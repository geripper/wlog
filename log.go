package wlog

import (
	// "encoding/json"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const (
	LevelEmergency = iota
	LevelAlert
	LevelCritical
	LevelError
	LevelWarning
	LevelNotice
	LevelInformational
	LevelDebug
)

const (
	levelLoggerImpl = -1
	AdapterFile     = "file"
)

const (
	LevelInfo  = LevelInformational
	LevelTrace = LevelDebug
	LevelWarn  = LevelWarning
)

type Logger interface {
	Init(config string) error
	WriteMsg(when time.Time, msg string, level int) error
	Destroy()
	Flush()
}

var levelPrefix = [LevelDebug + 1]string{"[M] ", "[A] ", "[C] ", "[E] ", "[W] ", "[N] ", "[I] ", "[D] "}

type WLogger struct {
	lock                sync.Mutex
	level               int
	init                bool
	enableFuncCallDepth bool
	loggerFuncCallDepth int
	asynchronous        bool
	msgChanLen          int64
	msgChan             chan *logMsg
	signalChan          chan string
	wg                  sync.WaitGroup
	outputs             *nameLogger
}

const defaultAsyncMsgLen = 1e3

type nameLogger struct {
	Logger
	name string
}

type logMsg struct {
	level int
	msg   string
	when  time.Time
}

var logMsgPool *sync.Pool

func NewLogger(channelLens ...int64) *WLogger {
	bl := new(WLogger)
	bl.level = LevelDebug
	bl.loggerFuncCallDepth = 2
	bl.msgChanLen = append(channelLens, 0)[0]
	if bl.msgChanLen <= 0 {
		bl.msgChanLen = defaultAsyncMsgLen
	}
	bl.signalChan = make(chan string, 1)
	// bl.SetLogger(AdapterFile)
	return bl
}

func (bl *WLogger) Async(msgLen ...int64) *WLogger {
	bl.lock.Lock()
	defer bl.lock.Unlock()
	if bl.asynchronous {
		return bl
	}
	bl.asynchronous = true
	if len(msgLen) > 0 && msgLen[0] > 0 {
		bl.msgChanLen = msgLen[0]
	}
	bl.msgChan = make(chan *logMsg, bl.msgChanLen)
	logMsgPool = &sync.Pool{
		New: func() interface{} {
			return &logMsg{}
		},
	}
	bl.wg.Add(1)
	go bl.startLogger()
	return bl
}

func (bl *WLogger) setLogger(adapterName string, configs ...string) error {
	config := append(configs, "{}")[0]

	lg := newFileWriter()
	err := lg.Init(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logs.SetLogger:"+err.Error())
		return err
	}

	bl.outputs = &nameLogger{name: adapterName, Logger: lg}
	return nil
}

func (bl *WLogger) SetLogger(adapterName string, configs ...string) error {
	bl.lock.Lock()
	defer bl.lock.Unlock()
	if !bl.init {
		bl.init = true
	}
	return bl.setLogger(adapterName, configs...)
}

//DelLogger 移除logger
func (bl *WLogger) DelLogger() error {
	bl.lock.Lock()
	defer bl.lock.Unlock()
	bl.outputs.Destroy()
	bl.outputs = nil
	return nil
}

func (bl *WLogger) writeToLoggers(when time.Time, msg string, level int) {
	err := bl.outputs.WriteMsg(when, msg, level)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to writeMsg to adapter:%v,error:%v\n", bl.outputs.name, err)
	}
}

func (bl *WLogger) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if p[len(p)-1] == '\n' {
		p = p[:len(p)-1]
	}

	err := bl.WriteMsg(levelLoggerImpl, string(p))
	if err == nil {
		return len(p), nil
	}

	return 0, err
}

func (bl *WLogger) WriteMsg(logLevel int, msg string, v ...interface{}) error {
	if !bl.init {
		bl.lock.Lock()
		bl.setLogger(AdapterFile)
		bl.lock.Unlock()
	}

	length := len(v)
	if length > 0 {
	    if strings.Contains(msg, "%") {
		msg = fmt.Sprintf(msg, v...)
	    } else {
		msg += fmt.Sprint(v...)
	    }
	}
	when := time.Now().Local()
	if bl.enableFuncCallDepth {
		_, file, line, ok := runtime.Caller(bl.loggerFuncCallDepth)
		if !ok {
			file = "???"
			line = 0
		}
		_, filename := path.Split(file)
		msg = "[" + filename + ":" + strconv.FormatInt(int64(line), 10) + "]" + msg
	}

	if logLevel == levelLoggerImpl {
		logLevel = LevelEmergency
	} else {
		msg = levelPrefix[logLevel] + msg
	}

	if bl.asynchronous {
		lm := logMsgPool.Get().(*logMsg)
		lm.level = logLevel
		lm.msg = msg
		lm.when = when
		bl.msgChan <- lm
	} else {
		bl.writeToLoggers(when, msg, logLevel)
	}

	return nil
}

func (bl *WLogger) SetLevel(l int) {
	bl.level = l
}

func (bl *WLogger) SetLogFuncCallDepth(d int) {
	bl.loggerFuncCallDepth = d
}

func (bl *WLogger) GetLogFuncCallDepth() int {
	return bl.loggerFuncCallDepth
}

func (bl *WLogger) EnableFuncCallDepth(b bool) {
	bl.enableFuncCallDepth = b
}

func (bl *WLogger) startLogger() {
	gameOver := false
	for {
		select {
		case bm := <-bl.msgChan:
			bl.writeToLoggers(bm.when, bm.msg, bm.level)
			logMsgPool.Put(bm)
		case sg := <-bl.signalChan:
			bl.flush()
			if sg == "close" {
				bl.outputs.Destroy()
				bl.outputs = nil
				gameOver = true
			}
			bl.wg.Done()
		}
		if gameOver {
			break
		}
	}
}

func (bl *WLogger) Emergency(format string, v ...interface{}) {
	if LevelEmergency > bl.level {
		return
	}
	bl.WriteMsg(LevelEmergency, format, v...)
}

func (bl *WLogger) Alert(format string, v ...interface{}) {
	if LevelAlert > bl.level {
		return
	}
	bl.WriteMsg(LevelAlert, format, v...)
}

func (bl *WLogger) Critical(format string, v ...interface{}) {
	if LevelCritical > bl.level {
		return
	}
	bl.WriteMsg(LevelCritical, format, v...)
}

func (bl *WLogger) Error(format string, v ...interface{}) {
	if LevelError > bl.level {
		return
	}
	bl.WriteMsg(LevelError, format, v...)
}

func (bl *WLogger) Warning(format string, v ...interface{}) {
	if LevelWarning > bl.level {
		return
	}
	bl.WriteMsg(LevelWarning, format, v...)
}

func (bl *WLogger) Notice(format string, v ...interface{}) {
	if LevelNotice > bl.level {
		return
	}
	bl.WriteMsg(LevelNotice, format, v...)
}

func (bl *WLogger) Informational(format string, v ...interface{}) {
	if LevelInformational > bl.level {
		return
	}
	bl.WriteMsg(LevelInformational, format, v...)
}

func (bl *WLogger) Debug(format string, v ...interface{}) {
	if LevelDebug > bl.level {
		return
	}
	bl.WriteMsg(LevelDebug, format, v...)
}

func (bl *WLogger) Warn(format string, v ...interface{}) {
	if LevelWarning > bl.level {
		return
	}
	bl.WriteMsg(LevelWarn, format, v...)
}

func (bl *WLogger) Info(format string, v ...interface{}) {
	if LevelInformational > bl.level {
		return
	}
	bl.WriteMsg(LevelInformational, format, v...)
}

func (bl *WLogger) Trace(format string, v ...interface{}) {
	if LevelDebug > bl.level {
		return
	}
	bl.WriteMsg(LevelTrace, format, v...)
}

func (bl *WLogger) Flush() {
	if bl.asynchronous {
		bl.signalChan <- "flush"
		bl.wg.Wait()
		bl.wg.Add(1)
		return
	}
	bl.flush()
}

func (bl *WLogger) Close() {
	if bl.asynchronous {
		bl.signalChan <- "close"
		bl.wg.Wait()
		close(bl.msgChan)
	} else {
		bl.flush()
		bl.outputs.Destroy()
		bl.outputs = nil
	}
	close(bl.signalChan)
}

func (bl *WLogger) Reset() {
	bl.Flush()
	bl.outputs.Destroy()
	bl.outputs = nil
}

func (bl *WLogger) flush() {
	if bl.asynchronous {
		for {
			if len(bl.msgChan) > 0 {
				bm := <-bl.msgChan
				bl.writeToLoggers(bm.when, bm.msg, bm.level)
				logMsgPool.Put(bm)
				continue
			}
			break
		}
	}
	bl.outputs.Flush()
}
