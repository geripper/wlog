package wlog

import (
	"io"
	"sync"
	"time"
)

type logWriter struct {
	sync.Mutex
	writer io.Writer
}

func newLogWriter(wr io.Writer) *logWriter {
	return &logWriter{writer: wr}
}

func (lg *logWriter) println(when time.Time, msg string) {
	lg.Lock()
	h, _ := formatTimeHeader(when)
	lg.writer.Write(append(append([]byte(h), msg...), '\n'))
	lg.Unlock()
}

func formatTimeHeader(when time.Time) (string, int) {
	now := when.Format("2006-01-02 15:04:05")
	return now + " ", when.Day()
}
