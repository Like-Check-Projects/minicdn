package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/codegangsta/cli"
)

var wslog *log.Logger

// var (
// 	mirror   = flag.String("mirror", "", "Mirror Web Base URL")
// 	logfile  = flag.String("log", "-", "Set log file, default STDOUT")
// 	upstream = flag.String("upstream", "", "Server base URL, conflict with -mirror")
// 	address  = flag.String("addr", ":5000", "Listen address")
// 	token    = flag.String("token", "1234567890ABCDEFG", "peer and master token should be same")
// 	cachedir = flag.String("cachedir", "cache", "Cache directory to store big files")
// )

func InitSignal() {
	sig := make(chan os.Signal, 10)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			s := <-sig
			fmt.Println("Got signal:", s)
			if state.IsClosed() {
				fmt.Println("Cold close !!!")
				os.Exit(1)
			}
			fmt.Println("Warm close, waiting ...")
			go func() {
				state.Close()
				os.Exit(0)
			}()
		}
	}()
}

func checkErr(er error) {
	if er != nil {
		log.Fatal(er)
	}
}

func createCliApp() *cli.App {
	app := cli.NewApp()
	app.Name = "minicdn"
	app.Usage = "type help for more information"

	// global flags
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "token",
			Value: "123456",
			Usage: "token that verify between master and slave",
		},
		cli.StringFlag{
			Name:  "cachedir",
			Value: "cache",
			Usage: "caeche dir which store big files",
		},
	}
	app.Action = func(c *cli.Context) {
		log.Println("Default action")
	}
	app.Commands = []cli.Command{
		{
			Name:  "master",
			Usage: "mater mode",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "mirror",
					Usage: "mirror http address",
				},
				cli.StringFlag{
					Name:  "logfile, l",
					Value: "-",
					Usage: "log file",
				},
				cli.StringFlag{
					Name:  "addr",
					Value: ":5000",
					Usage: "listen address",
				},
			},
			Action: masterAction,
		},
		{
			Name:  "slave",
			Usage: "slave mode",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "master-addr, m",
					Usage: "master listen address",
				},
				cli.StringFlag{
					Name:  "addr",
					Value: ":5000",
					Usage: "listen address",
				},
			},
			Action: slaveAction,
		},
	}
	return app
}

func masterAction(c *cli.Context) {
	println("Master mode")
	mirror := c.String("mirror")
	if mirror == "" {
		log.Fatal("mirror option required")
	}
	if _, err := url.Parse(mirror); err != nil {
		log.Fatal(err)
	}

	logfile := c.String("logfile")

	if logfile == "-" || logfile == "" {
		wslog = log.New(os.Stderr, "", log.LstdFlags)
	} else {
		fd, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			log.Fatal(err)
		}
		wslog = log.New(fd, "", 0)
	}

	http.HandleFunc(defaultWSURL, NewWsHandler(mirror, wslog))
	http.HandleFunc("/", NewFileHandler(true, mirror, c.GlobalString("cachedir")))
	http.HandleFunc("/_log", func(w http.ResponseWriter, r *http.Request) {
		if logfile == "" || logfile == "-" {
			http.Error(w, "Log file not found", 404)
			return
		}
		http.ServeFile(w, r, logfile)
	})

	listenAddr := c.String("addr")
	log.Printf("Listening on %s", listenAddr)
	InitSignal()
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func slaveAction(c *cli.Context) {
	cachedir := c.GlobalString("cachedir")
	token := c.GlobalString("token")
	listenAddr := c.String("addr")
	masterAddr := c.String("master-addr")
	if _, err := os.Stat(cachedir); os.IsNotExist(err) {
		er := os.MkdirAll(cachedir, 0755)
		checkErr(er)
	}

	if err := InitPeer(masterAddr, listenAddr, cachedir, token); err != nil {
		log.Fatal(err)
	}
	log.Printf("Listening on %s", listenAddr)
	InitSignal()
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func main() {
	app := createCliApp()
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
