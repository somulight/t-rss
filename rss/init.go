package rss

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/capric98/GoRSS/client"
)

var (
	oLock    = sync.Mutex{}
	DMode    bool
	Config   string
	TestOnly bool
	CDir     string
)

func checkRegexp(v RssRespType, reg []*regexp.Regexp) bool {
	for _, r := range reg {
		if r.MatchString(v.Title) {
			return true
		}
		// matched, _ = regexp.MatchString(r, v.Description)
		// if matched {
		// 	return true
		// }
		if r.MatchString(v.Author) {
			return true
		}
	}
	return false
}

func saveItem(r RssRespType, t TaskType) {
	nClient := http.Client{
		Timeout: time.Duration(10 * time.Second),
	}
	startT := time.Now()

	resp, err := nClient.Get(r.DURL)
	if err != nil {
		LevelPrintLog(fmt.Sprintf("%v\n", err), true)
		return
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		LevelPrintLog(fmt.Sprintf("%v\n", err), true)
	}

	if TestOnly {
		PrintTimeInfo(fmt.Sprintf("Item \"%s\" done.", r.Title), time.Since(startT))
		return
	}

	if t.DownPath != "" {
		err := ioutil.WriteFile(t.DownPath+string(os.PathSeparator)+GetFileInfo(r.DURL, resp.Header), body, 0644)
		if err != nil {
			LevelPrintLog(fmt.Sprintf("Warning: %v\n", err), true)
			return
		}
		LevelPrintLog(fmt.Sprintf("Item \"%s\" is saved as \"%s\"\n", r.Title, GetFileInfo(r.DURL, resp.Header)), false)
	}

	// Add file to client.
	if t.Client != nil {
		for _, v := range t.Client {
			for ec := 0; ec < 3; ec++ {
				switch v.Name {
				case "qBittorrent":
					if ec != 0 {
						_ = v.Client.(client.QBType).Init()
					} // In case of the session timeout, reinitiallize it.
					err = v.Client.(client.QBType).Add(body, GetFileInfo(r.DURL, resp.Header))
				case "Deluge":
					//err= v.Client.(client.DeType).Add(body)
				}
				if err != nil {
					// If fail to add torrent to client, try another 2 times.
					LevelPrintLog(fmt.Sprintf("%s: Failed to add item \"%s\" to %s client with message: \"%v\".\n", t.TaskName, r.Title, v.Name, err), true)
				} else {
					break
				}
				if ec == 2 {
					return
					// Failed 3 times, quit and do not save history.
				}

			}
		}
	}
	PrintTimeInfo(fmt.Sprintf("Item \"%s\" done.", r.Title), time.Since(startT))

	if !TestOnly {
		if f, err := os.Create(CDir + r.GUID); err != nil {
			LevelPrintLog(fmt.Sprintf("Warning: %v", err), true)
		} else {
			f.Close()
		}
		// Under test only mode, we do not create history file.
	}
}

func runTask(t TaskType) {
	client := http.Client{
		Timeout: time.Duration(10 * time.Second),
	}
	for {
		LevelPrintLog(fmt.Sprintf("Run task: %s.\n", t.TaskName), true)
		startT := time.Now()

		Rresp, err := fetch(t.RSSLink, &client)
		if err != nil {
			LevelPrintLog(fmt.Sprintf("Caution: Task %s failed to get RSS data and raised an error: %v.\n", t.TaskName, err), true)
			time.Sleep(time.Duration(t.Interval) * time.Second)
			continue
		}
		acCount := 0
		rjCount := 0
		for _, v := range Rresp {
			// Check if item had been accepted yet.
			if v.GUID == "" {
				v.GUID = NameRegularize(v.Title)
				if len(v.GUID) > 200 {
					v.GUID = v.GUID[:200]
				}
			} else {
				v.GUID = NameRegularize(v.GUID)
			} // Just in case.
			if _, err := os.Stat(CDir + v.GUID); !os.IsNotExist(err) {
				rjCount++
				continue
			}

			// Check regexp filter.
			if (t.RjcRegexp != nil) && (checkRegexp(v, t.RjcRegexp)) {
				LevelPrintLog(fmt.Sprintf("%s: Reject item \"%s\"\n", t.TaskName, v.Title), true)
				rjCount++
				continue
			}
			if t.AccRegexp != nil && (!checkRegexp(v, t.AccRegexp)) && (t.Strict) {
				LevelPrintLog(fmt.Sprintf("%s: Cannot accept item \"%s\" due to strict mode.\n", t.TaskName, v.Title), true)
				rjCount++
				continue
			}

			// Check content_size.
			if !((v.Length > t.MinSize && v.Length < t.MaxSize) || (v.Length == 0 && !t.Strict)) {
				LevelPrintLog(fmt.Sprintf("%s: Reject item \"%s\" due to content_size not fit.\n", t.TaskName, v.Title), true)
				LevelPrintLog(fmt.Sprintf("%d vs [%d,%d]\n", v.Length, t.MinSize, t.MaxSize), false)
				rjCount++
				continue
			}

			LevelPrintLog(fmt.Sprintf("%s: Accept item \"%s\"\n", t.TaskName, v.Title), true)
			acCount++

			go saveItem(v, t)

		}
		PrintTimeInfo(fmt.Sprintf("Task %s: Accept %d item(s), reject %d item(s).", t.TaskName, acCount, rjCount), time.Since(startT))
		time.Sleep(time.Duration(t.Interval) * time.Second)
	}
}

func Init() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	if DMode {
		LevelPrintLog(fmt.Sprintf("Debug: %t\n", DMode), true)
		LevelPrintLog(fmt.Sprintf("Test: %t\n", TestOnly), true)
		LevelPrintLog(fmt.Sprintf("Config: %s\n", Config), true)
		LevelPrintLog(fmt.Sprintf("History: %s\n", CDir), true)
	}

	cdata, err := ioutil.ReadFile(Config)
	if err != nil {
		LevelPrintLog(fmt.Sprintf("Error: %v\n", err), true)
		os.Exit(2)
	}

	if Config != "config.yml" && CDir == ".RSS-saved" {
		// Change config file path without setting CDir.
		CDir = filepath.Dir(Config) + string(os.PathSeparator) + ".RSS-saved" + string(os.PathSeparator)
	}
	CDir = filepath.Dir(CDir+string(os.PathSeparator)) + string(os.PathSeparator) // Just in case.
	LevelPrintLog(fmt.Sprintf("History will be saved to: %s\n", CDir), false)
	if _, err := os.Stat(CDir); os.IsNotExist(err) {
		merr := os.Mkdir(CDir, 0644)
		if merr != nil {
			LevelPrintLog(fmt.Sprintf("%v\n", merr), true)
			os.Exit(2)
		} else {
			LevelPrintLog(fmt.Sprintf("%s did not exist, make it!", CDir), false)
		}

	}
	taskList := parseSettings(cdata)
	if TestOnly {
		log.Println(Config, "passes the test.")
		//return
	}

	qsignal := make(chan error, 2)
	go func() {
		c := make(chan os.Signal, 10) // bufferd
		signal.Notify(c, os.Interrupt)
		qsignal <- fmt.Errorf("%s", <-c)
	}()

	for _, t := range taskList {
		go runTask(t)
	}
	go cleanDaemon()
	<-qsignal
	LevelPrintLog(fmt.Sprintf("Receive signal 2, quit the program.\n"), true)
	LevelPrintLog(fmt.Sprintf("Bye.\n"), true)
}
