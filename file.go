package wlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fileLogWriter struct {
	sync.RWMutex        //write log order by order and  atomic incr maxLinesCurLines and maxSizeCurSize
	Filename     string `json:"filename"`
	fileWriter   *os.File
	Day          int `json:"day"`

	MaxLines         int `json:"maxlines"`
	maxLinesCurLines int

	MaxSize        int `json:"maxsize"`
	maxSizeCurSize int

	Daily         bool `json:"daily"`
	dailyOpenDate int
	dailyOpenTime time.Time

	Rotate bool `json:"rotate"`

	Level int    `json:"level"`
	Perm  string `json:"perm"`

	RotatePerm string `json:"rotateperm"`

	filePath             string `json:"file_path"`
	fileNameOnly, suffix string
}

func newFileWriter() Logger {
	return &fileLogWriter{
		Daily:      true,
		Day:        7,
		Rotate:     true,
		RotatePerm: "0666",
		Level:      LevelTrace,
		Perm:       "0666",
	}
}

func (w *fileLogWriter) Init(jsonConfig string) error {
	err := json.Unmarshal([]byte(jsonConfig), w)
	if err != nil {
		return err
	}

	if len(w.Filename) == 0 {
		return errors.New("must have filename")
	}
	w.suffix = filepath.Ext(w.Filename)
	w.filePath = filepath.Dir(w.Filename)
	w.fileNameOnly = strings.TrimSuffix(w.Filename, w.suffix)
	if w.suffix == "" {
		w.suffix = ".log"
	}
	if w.Day == 0 {
		w.Day = 7
	}

	err = w.startLogger()
	return err
}

func (w *fileLogWriter) startLogger() error {
	file, err := w.createLogFile()
	if err != nil {
		return err
	}

	if w.fileWriter != nil {
		w.fileWriter.Close()
	}

	w.fileWriter = file

	return w.initFd()
}

func (w *fileLogWriter) needRotate(size, day int) bool {
	return (w.MaxLines > 0 && w.maxLinesCurLines >= w.MaxLines) ||
		(w.MaxSize > 0 && w.maxSizeCurSize >= w.MaxSize) || (w.Daily && day != w.dailyOpenDate && w.maxLinesCurLines > 0)
}

func (w *fileLogWriter) WriteMsg(when time.Time, msg string, level int) error {
	if level > w.Level {
		return nil
	}

	h, day := formatTimeHeader(when)
	msg = h + msg + "\n"
	if w.Rotate {
		w.RLock()
		if w.needRotate(len(msg), day) {
			w.RUnlock()
			w.Lock()
			if w.needRotate(len(msg), day) {
				if err := w.doRotate(when); err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.Filename, err)
				}
			}
			w.Unlock()
		} else {
			w.RUnlock()
		}
	}

	w.Lock()
	_, err := w.fileWriter.Write([]byte(msg))
	if err == nil {
		w.maxLinesCurLines++
		w.maxSizeCurSize += len(msg)
	}
	w.Unlock()
	return err
}

func (w *fileLogWriter) createLogFile() (*os.File, error) {
	perm, err := strconv.ParseInt(w.Perm, 8, 64)
	if err != nil {
		return nil, err
	}
	fd, err := os.OpenFile(w.Filename, os.O_WRONLY|os.O_APPEND|os.O_CREATE, os.FileMode(perm))
	if err == nil {
		os.Chmod(w.Filename, os.FileMode(perm))
	}
	return fd, err
}

func (w *fileLogWriter) initFd() error {
	fd := w.fileWriter
	fInfo, err := fd.Stat()
	if err != nil {
		return fmt.Errorf("get stat err: %s\n", err)
	}

	w.maxSizeCurSize = int(fInfo.Size())
	w.dailyOpenTime = time.Now().Local()
	w.dailyOpenDate = w.dailyOpenTime.Day()
	w.maxLinesCurLines = 0
	if w.Daily {
		go w.dailyRotate(w.dailyOpenTime)
		go w.taskDeleteLog()
	}

	if fInfo.Size() > 0 && w.MaxLines > 0 {
		count, err := w.lines()
		if err != nil {
			return err
		}
		w.maxLinesCurLines = count
	}
	return nil
}

func (w *fileLogWriter) dailyRotate(openTime time.Time) {
	now := time.Now().Local()
	y, m, d := openTime.Add(24 * time.Hour).Date()
	nextDay := time.Date(y, m, d, 0, 0, 0, 0, openTime.Location())
	tm := time.NewTimer(time.Duration(nextDay.UnixNano() - openTime.UnixNano() + 60))
	<-tm.C
	w.Lock()
	if w.needRotate(0, now.Day()) {
		if err := w.doRotate(now); err != nil {
			fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.Filename, err)
		}
	}
	w.Unlock()
}

func (w *fileLogWriter) lines() (int, error) {
	fd, err := os.Open(w.Filename)
	if err != nil {
		return 0, err
	}
	defer fd.Close()

	buf := make([]byte, 32768) //32k
	count := 0
	lineSep := []byte{'\n'}

	for {
		c, err := fd.Read(buf)
		if err != nil && err != io.EOF {
			return count, err
		}

		count += bytes.Count(buf[:c], lineSep)

		if err == io.EOF {
			break
		}
	}

	return count, nil
}

func (w *fileLogWriter) doRotate(logTime time.Time) error {
	// file exists
	// Find the next available number
	num := 1
	fName := ""
	rotatePerm, err := strconv.ParseInt(w.RotatePerm, 8, 64)
	if err != nil {
		return err
	}

	_, err = os.Lstat(w.Filename)
	if err != nil {
		goto RESTART_LOGGER
	}

	if w.MaxLines > 0 || w.MaxSize > 0 {
		for ; err == nil && num <= 999; num++ {
			fName = w.fileNameOnly + fmt.Sprintf(".%s.%03d%s", logTime.Format("2006-01-02"), num, w.suffix)
			_, err = os.Lstat(fName)
		}
	} else {
		fName = fmt.Sprintf("%s.%s%s", w.fileNameOnly, w.dailyOpenTime.Format("2006-01-02"), w.suffix)
		_, err = os.Lstat(fName)
		for ; err == nil && num <= 999; num++ {
			fName = w.fileNameOnly + fmt.Sprintf(".%s.%03d%s", w.dailyOpenTime.Format("2006-01-02"), num, w.suffix)
			_, err = os.Lstat(fName)
		}
	}

	// return error if the last file checked still existed
	if err == nil {
		return fmt.Errorf("Rotate: Cannot find free log number to rename %s:%s\n", w.Filename, err.Error())
	}

	// close fileWriter before rename
	w.fileWriter.Close()

	// Rename the file to its new found name
	// even if occurs error,we MUST guarantee to  restart new logger
	err = os.Rename(w.Filename, fName)
	if err != nil {
		goto RESTART_LOGGER
	}
	err = os.Chmod(fName, os.FileMode(rotatePerm))

RESTART_LOGGER:
	startLoggerErr := w.startLogger()

	if startLoggerErr != nil {
		return fmt.Errorf("Rotate StartLogger: %s\n", startLoggerErr)
	}
	if err != nil {
		return fmt.Errorf("Rotate: %s\n", err)
	}
	return nil
}

func (w *fileLogWriter) Destroy() {
	w.fileWriter.Close()
}

func (w *fileLogWriter) Flush() {
	w.fileWriter.Sync()
}

func (w *fileLogWriter) taskDeleteLog() {
	day := strconv.Itoa(w.Day)
	var output []byte
	var err error
	d := time.Now()
	date := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
	diff := (date.Unix() + 86400) - d.Unix()
	t := time.NewTimer(time.Duration(diff) * time.Second)

	goos := runtime.GOOS
	fmt.Println("日志路径:", w.filePath)
	for {
		<-t.C

		if goos == "windows" {
			execArr := []string{"/c", "forfiles", "-p", w.filePath, "-s", "-m", "*", "-d", "-" + day,
				"-c", "cmd /c del /q /f @path"}

			output, err = exec.Command("cmd", execArr...).CombinedOutput()
		} else {
			execName := `find ` + w.filePath + `/ -ctime +` + day + ` -name "*" -exec rm -rf {} \;`

			fmt.Println("执行命令:", execName)
			output, err = exec.Command("/bin/bash", "-c", execName).CombinedOutput()
		}

		fmt.Println("执行结果:", string(output), err)
		t.Reset(24 * time.Hour)
	}
}
