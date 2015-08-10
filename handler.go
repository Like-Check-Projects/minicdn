package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/codeskyblue/groupcache"
	"github.com/ugorji/go/codec"
)

var (
	thumbNails = groupcache.NewGroup("thumbnail", MAX_MEMORY_SIZE*2, groupcache.GetterFunc(
		func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
			fileName := key
			bytes, err := generateThumbnail(fileName)
			if err != nil {
				return err
			}
			dest.SetBytes(bytes)
			return nil
		}))
)

const (
	HR_TYPE_FILE = 1
	HR_TYPE_BYTE = 2

	MAX_MEMORY_SIZE = 64 << 20
)

type HttpResponse struct {
	Header http.Header
	Type   int
	Body   []byte
}

type ErrorWithStatus struct {
	Msg  string
	Code int
}

func (e *ErrorWithStatus) Error() string {
	return e.Msg
}

func Md5str(v string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(v)))
}

func generateThumbnail(key string) ([]byte, error) {
	u, _ := url.Parse(*mirror)
	u.Path = key
	fmt.Println("download:", key)
	resp, err := http.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, &ErrorWithStatus{string(body), resp.StatusCode}
	}

	var length int64
	_, err = fmt.Sscanf(resp.Header.Get("Content-Length"), "%d", &length)
	if length > MAX_MEMORY_SIZE {
		filename := filepath.Join(*cachedir, Md5str(key))
		tmpname := filename + ".xxx.download"
		finfo, err := os.Stat(filename)
		var download bool
		switch {
		case err != nil:
			download = true
		case finfo.Size() != length:
			download = true
		default:
			download = false
		}
		fmt.Println("download:", download)
		if download {
			fd, err := os.Create(tmpname)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(fd, resp.Body)
			if err != nil {
				fd.Close()
				return nil, err
			}
			fd.Close()
			os.Rename(tmpname, filename)
		}
		buf := bytes.NewBuffer(nil)
		mpenc := codec.NewEncoder(buf, &codec.MsgpackHandle{})
		err = mpenc.Encode(HttpResponse{resp.Header, HR_TYPE_FILE, []byte(filename)})
		return buf.Bytes(), err
	} else {
		buf := bytes.NewBuffer(nil)
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		mpenc := codec.NewEncoder(buf, &codec.MsgpackHandle{})
		err = mpenc.Encode(HttpResponse{resp.Header, HR_TYPE_BYTE, body})
		return buf.Bytes(), err
	}
}

func FileHandler(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path

	state.addActiveDownload(1)
	defer state.addActiveDownload(-1)

	if *upstream == "" { // Master
		if peerAddr, err := peerGroup.PeekPeer(); err == nil {
			u, _ := url.Parse(peerAddr)
			u.Path = r.URL.Path
			u.RawQuery = r.URL.RawQuery
			http.Redirect(w, r, u.String(), 302)
			return
		}
	}
	var data []byte
	var ctx groupcache.Context
	err := thumbNails.Get(ctx, key, groupcache.AllocatingByteSliceSink(&data))
	if err != nil {
		if es, ok := err.(*ErrorWithStatus); ok {
			http.Error(w, es.Msg, es.Code)
		} else {
			http.Error(w, err.Error(), 500)
		}
		return
	}

	fmt.Printf("key: %s, len(data): %d, addr: %p\n", key, len(data), &data[0]) //addr: %p\n", key, &data[0])
	var hr HttpResponse
	mpdec := codec.NewDecoder(bytes.NewReader(data), &codec.MsgpackHandle{})
	err = mpdec.Decode(&hr)
	if err != nil {
		if es, ok := err.(*ErrorWithStatus); ok {
			http.Error(w, es.Msg, es.Code)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	// FIXME(ssx): should have some better way
	for key, _ := range hr.Header {
		w.Header().Set(key, hr.Header.Get(key))
	}

	sendData := map[string]interface{}{
		"remote_addr": r.RemoteAddr,
		"key":         key,
		"success":     err == nil,
		"user_agent":  r.Header.Get("User-Agent"),
	}
	headerData := r.Header.Get("X-Minicdn-Data")
	headerType := r.Header.Get("X-Minicdn-Type")
	if headerType == "json" {
		var data interface{}
		err := json.Unmarshal([]byte(headerData), &data)
		if err == nil {
			sendData["header_data"] = data
			sendData["header_type"] = headerType
		} else {
			log.Println("header data decode:", err)
		}
	} else {
		sendData["header_data"] = headerData
		sendData["header_type"] = headerType
	}

	if *upstream != "" { // Slave
		sendc <- sendData
	}
	modTime, err := time.Parse(http.TimeFormat, hr.Header.Get("Last-Modified"))
	if err != nil {
		modTime = time.Now()
	}

	switch hr.Type {
	case HR_TYPE_BYTE:
		rd := bytes.NewReader(hr.Body)
		http.ServeContent(w, r, filepath.Base(key), modTime, rd)
	case HR_TYPE_FILE:
		fmt.Println("From file")
		rd, err := os.Open(string(hr.Body))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rd.Close()
		http.ServeContent(w, r, filepath.Base(key), modTime, rd)
	default:
		http.Error(w, fmt.Sprintf("Header type unknown: %d", hr.Type), 500)
	}
}

func LogHandler(w http.ResponseWriter, r *http.Request) {
	if *logfile == "" || *logfile == "-" {
		http.Error(w, "Log file not found", 404)
		return
	}
	http.ServeFile(w, r, *logfile)
}

func init() {
	http.HandleFunc("/", FileHandler)
	http.HandleFunc("/_log", LogHandler)
}
