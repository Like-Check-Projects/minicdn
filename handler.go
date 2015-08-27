package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/codeskyblue/groupcache"
)

var (
	cacheGroup *groupcache.Group
	// thumbNails = groupcache.NewGroup("thumbnail", MAX_MEMORY_SIZE*2, groupcache.GetterFunc(
	// 	func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
	// 		bytes, err := downloadThumbnail(key)
	// 		if err != nil {
	// 			return err
	// 		}
	// 		dest.SetBytes(bytes)
	// 		return nil
	// 	}))
	// cachedir string
	// mirror string
)

const (
	// Error Response Type
	ER_TYPE_FILE = 1
	ER_TYPE_HTML = 2

	// Header Type
	HT_TYPE_JSON = "json"
	HT_TYPE_TEXT = "text"

	MAX_MEMORY_SIZE = 64 << 20
)

type HttpResponse struct {
	Header     http.Header
	BodyData   []byte
	StatusCode int

	key      string
	basePath string
	metaPath string
	bodyPath string
	tempPath string

	cachedir string
}

func (hr *HttpResponse) setKey(key string) {
	if hr.key != key {
		hr.key = key
		hr.basePath = Md5str(key)
		hr.bodyPath = filepath.Join(hr.cachedir, hr.basePath+".body")
		hr.metaPath = filepath.Join(hr.cachedir, hr.basePath+".meta")
		hr.tempPath = hr.bodyPath + fmt.Sprintf(".%d.temp.download", rand.Int())
	}
}

func (hr *HttpResponse) LoadMeta(key string) (err error) {
	hr.setKey(key)
	bodybak := hr.BodyData
	defer func() {
		hr.BodyData = bodybak
	}()
	meta, err := ioutil.ReadFile(hr.metaPath)
	if err != nil {
		return err
	}
	return GobDecode(meta, hr)
}

func (hr *HttpResponse) DumpMeta(key string) error {
	hr.setKey(key)
	var nhr HttpResponse = *hr
	nhr.BodyData = nil
	data, err := GobEncode(nhr)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(hr.metaPath, data, 0644)
}

// Helper func for Encode and Decode
func GobEncode(v interface{}) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	enc := gob.NewEncoder(buf)
	err := enc.Encode(v)
	return buf.Bytes(), err
}

func GobDecode(data []byte, v interface{}) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	return dec.Decode(v)
}

type ErrorWithResponse struct {
	Resp *HttpResponse
	Type int
}

func (e *ErrorWithResponse) Error() string {
	return fmt.Sprintf("Specified for groupcache.Getter, type: %d", e.Type)
}

func downloadThumbnail(mirror string, cachedir string, key string) ([]byte, error) {
	u, _ := url.Parse(mirror)
	u.Path = key
	//fmt.Println("thumbnail:", key)
	resp, err := http.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// HTTP status != 200, not cache it.
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, &ErrorWithResponse{
			Resp: &HttpResponse{
				BodyData:   body,
				Header:     resp.Header,
				StatusCode: resp.StatusCode,
				cachedir:   cachedir,
			},
			Type: ER_TYPE_HTML,
		}
	}

	// If no length provided, maybe this is a big file
	var length int64
	_, err = fmt.Sscanf(resp.Header.Get("Content-Length"), "%d", &length)
	// log.Printf("key: %s, length: %d", key, length)

	if err != nil || length > MAX_MEMORY_SIZE {
		var hr = HttpResponse{cachedir: cachedir}
		hr.setKey(key)

		finfo, err := os.Stat(hr.bodyPath)
		var download = false
		if err != nil || finfo.Size() != length {
			download = true
		}

		// Save big data to file
		//fmt.Println("download:", download)
		if download {
			fd, err := os.Create(hr.tempPath)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(fd, resp.Body)
			if err != nil {
				fd.Close()
				os.Remove(hr.tempPath)
				return nil, err
			}
			fd.Close()
			os.Rename(hr.tempPath, hr.bodyPath) // body
			var hr = &HttpResponse{
				Header:     resp.Header,
				StatusCode: http.StatusOK,
				cachedir:   cachedir,
			}
			if err := hr.DumpMeta(key); err != nil { // meta
				return nil, err
			}
		}
		return nil, &ErrorWithResponse{
			Type: ER_TYPE_FILE,
			Resp: nil,
		}
	} else {
		// Here only handle small file
		bodydata, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		var hr = &HttpResponse{Header: resp.Header, BodyData: bodydata, StatusCode: resp.StatusCode, cachedir: cachedir}
		return GobEncode(hr)
	}
}

func NewFileHandler(isMaster bool, mirror string, cachedir string) func(w http.ResponseWriter, r *http.Request) {
	cacheGroup = groupcache.NewGroup("thumbnail", MAX_MEMORY_SIZE*2, groupcache.GetterFunc(
		func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
			bytes, err := downloadThumbnail(mirror, cachedir, key)
			if err != nil {
				return err
			}
			dest.SetBytes(bytes)
			return nil
		}))

	return func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path

		state.addActiveDownload(1)
		defer state.addActiveDownload(-1)

		if isMaster {
			// redirect to slaves
			if peerAddr, err := peerGroup.PeekPeer(); err == nil {
				u, _ := url.Parse(peerAddr)
				u.Path = r.URL.Path
				u.RawQuery = r.URL.RawQuery
				http.Redirect(w, r, u.String(), 302)
				return
			}
		} else {
			sendStats(r) // stats send to master
		}
		serveContent(key, cachedir, w, r)
	}
}

func sendStats(r *http.Request) {
	data := map[string]interface{}{
		"remote_addr": r.RemoteAddr,
		"key":         r.URL.Path,
		"user_agent":  r.Header.Get("User-Agent"),
	}
	headerData := r.Header.Get("X-Minicdn-Data")
	headerType := r.Header.Get("X-Minicdn-Type")
	if headerType == HT_TYPE_JSON {
		var hdata interface{}
		err := json.Unmarshal([]byte(headerData), &hdata)
		if err == nil {
			data["header_data"] = hdata
			data["header_type"] = headerType
		} else {
			log.Println("header data decode:", err)
		}
	} else {
		data["header_data"] = headerData
		data["header_type"] = headerType
	}
	sendc <- data
}

func serveContent(key string, cachedir string, w http.ResponseWriter, r *http.Request) {
	var err error
	var hr = HttpResponse{cachedir: cachedir}
	var rd io.ReadSeeker
	var data []byte
	var ctx groupcache.Context

	// Read Local File
	if err = hr.LoadMeta(key); err == nil {
		fmt.Printf("load local file: %s\n", key)
		bodyfd, er := os.Open(hr.bodyPath)
		if er != nil {
			http.Error(w, er.Error(), 500)
			return
		}
		defer bodyfd.Close()
		rd = bodyfd
		goto SERVE_CONTENT
	}

	// Read Groupcache
	err = cacheGroup.Get(ctx, key, groupcache.AllocatingByteSliceSink(&data))
	if err == nil {
		// FIXME(ssx): use gob is not a good way.
		// It will create new space for hr.BodyData which will use too much memory.
		if err = GobDecode(data, &hr); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		rd = bytes.NewReader(hr.BodyData)
		fmt.Printf("groupcache: %s, len(data): %d, addr: %p\n", key, len(data), &data[0])
		goto SERVE_CONTENT
	}
	// Too big file will not use groupcache memory storage
	if es, ok := err.(*ErrorWithResponse); ok {
		switch es.Type {
		case ER_TYPE_FILE: // Read local file again
			if er := hr.LoadMeta(key); er != nil {
				http.Error(w, er.Error(), 500)
				return
			}
			bodyfd, er := os.Open(hr.bodyPath)
			if er != nil {
				http.Error(w, er.Error(), 500)
				return
			}
			defer bodyfd.Close()
			rd = bodyfd
			goto SERVE_CONTENT
		case ER_TYPE_HTML:
			w.WriteHeader(es.Resp.StatusCode)
			for name, _ := range hr.Header {
				w.Header().Set(name, es.Resp.Header.Get(key))
			}
			w.Write(es.Resp.BodyData)
			return
		default:
			log.Println("unknown es.Type:", es.Type)
		}
	}
	// Handle groupcache error
	http.Error(w, err.Error(), 500)
	return

SERVE_CONTENT:
	// FIXME(ssx): should have some better way to set header
	// header and modTime
	for key, _ := range hr.Header {
		w.Header().Set(key, hr.Header.Get(key))
	}
	modTime, err := time.Parse(http.TimeFormat, hr.Header.Get("Last-Modified"))
	if err != nil {
		modTime = time.Now()
	}
	http.ServeContent(w, r, filepath.Base(key), modTime, rd)
}

// func LogHandler(w http.ResponseWriter, r *http.Request) {
// 	if *logfile == "" || *logfile == "-" {
// 		http.Error(w, "Log file not found", 404)
// 		return
// 	}
// 	http.ServeFile(w, r, *logfile)
// }

// func init() {
// http.HandleFunc("/",NewFileHandler(isMaster, cachedir) FileHandler)
// http.HandleFunc("/_log", LogHandler)
// }
