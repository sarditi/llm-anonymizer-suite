package logger

import (
	"ext-llm-gateway/models"

	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Log levels
const (
	DEBUG = iota
	INFO
	ERROR
	FATAL
)

var (
	currentLevel = DEBUG
	levelMap     = map[string]int{
		"DEBUG": DEBUG,
		"INFO":  INFO,
		"ERROR": ERROR,
		"FATAL": FATAL,
	}
)

// Global logger
var Log *Logger

// Logger struct supports optional workerID
type Logger struct {
	workerID    int
	threadID    int
    workerName  string
    workerGroup string

}

func init() {
	log.SetOutput(os.Stdout)
	log.SetFlags(0)
	Log = &Logger{}
}

func (l *Logger) WithWorker(dMI *models.DispatcherMandatoryInputs) *Logger {
    return &Logger{
        workerID:    dMI.WorkerID + 1,
        threadID:    dMI.ThreadID,
        workerName:  dMI.WorkerName,
        workerGroup: dMI.WorkerGroup,
    }
}

// Debug prints a debug message
func (l *Logger) Debug(msg string) string { return l.print("DEBUG", msg)}
func (l *Logger) Info(msg string)  string  { return l.print("INFO", msg) }
func (l *Logger) Error(msg string)  string { return l.print("ERROR", msg) }
func (l *Logger) Fatal(msg string)  string { return l.print("FATAL", msg) }

// SetLevel sets the current global log level
func SetLevel(level string) {
	lvl, ok := levelMap[strings.ToUpper(level)]
	if ok {
		currentLevel = lvl
	} else {
		log.Printf("Invalid log level %s, using default DEBUG", level)
	}
}

// Internal print method
func (l *Logger) print(level string, msg string) string {
	if _, ok := levelMap[strings.ToUpper(level)]; !ok {
		level = "DEBUG"
	}

	if !shouldPrint(level) {
		return ""
	}

	timestamp := time.Now().Format("2006/01/02 15:04:05")
	if l.workerID != 0 {
		logMsg := fmt.Sprintf("[%s] [%s] [WorkerGroup %s][WrkThrdID %d-%d]: %s", timestamp, level, l.workerGroup ,l.workerID-1,l.threadID, msg)
		log.Println(logMsg)
		return logMsg
	} else {
		logMsg := fmt.Sprintf("[%s] [%s]: %s", timestamp, level, msg)
		log.Println(logMsg)
		return logMsg
	}

}

// shouldPrint implements log level filtering
func shouldPrint(level string) bool {
	switch currentLevel {
	case DEBUG:
		return true
	case INFO:
		return level == "INFO" || level == "ERROR" || level == "FATAL"
	case ERROR:
		return level == "ERROR" || level == "FATAL"
	case FATAL:
		return level == "FATAL"
	default:
		return true
	}
}
