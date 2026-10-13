package logger

import (
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ipv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var ipv6Pattern = regexp.MustCompile(`[0-9a-fA-F:]{3,45}`)

var (
	countryMu sync.Mutex
	countries = map[string]string{}
)

const maxRememberedIPs = 4096

// RememberCountry records the country a trusted proxy reported for an address,
// so any log line that mentions that address prints the country instead. The
// map lives in memory only and is emptied when it grows large.
func RememberCountry(ip, country string) {
	if ip == "" || country == "" {
		return
	}
	countryMu.Lock()
	defer countryMu.Unlock()
	if len(countries) >= maxRememberedIPs {
		countries = map[string]string{}
	}
	countries[ip] = country
}

func countryOf(ip string) string {
	countryMu.Lock()
	defer countryMu.Unlock()
	if c, ok := countries[ip]; ok {
		return c
	}
	return "??"
}

func redact(msg string) string {
	msg = ipv4Pattern.ReplaceAllStringFunc(msg, countryOf)
	return ipv6Pattern.ReplaceAllStringFunc(msg, func(m string) string {
		if strings.Contains(m, ":") {
			if ip := net.ParseIP(m); ip != nil {
				return countryOf(ip.String())
			}
		}
		return m
	})
}

var std = log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)

const recentKept = 100

// Entry is one remembered warning or error line.
type Entry struct {
	At    time.Time
	Level string
	Msg   string
}

var (
	recentMu sync.Mutex
	recent   []Entry
)

func remember(level, msg string) {
	recentMu.Lock()
	defer recentMu.Unlock()
	if len(recent) >= recentKept {
		copy(recent, recent[1:])
		recent = recent[:recentKept-1]
	}
	recent = append(recent, Entry{At: time.Now(), Level: strings.TrimSpace(level), Msg: msg})
}

// Recent returns the remembered warnings and errors, newest first. The lines
// are the redacted ones that were written to the log and live in memory only.
func Recent() []Entry {
	recentMu.Lock()
	defer recentMu.Unlock()
	out := make([]Entry, len(recent))
	for i, e := range recent {
		out[len(recent)-1-i] = e
	}
	return out
}

func output(level, format string, args ...any) {
	msg := redact(fmt.Sprintf(format, args...))
	if level != "INFO " {
		remember(level, msg)
	}
	std.Output(3, level+" "+msg)
}

func Info(format string, args ...any)  { output("INFO ", format, args...) }
func Warn(format string, args ...any)  { output("WARN ", format, args...) }
func Error(format string, args ...any) { output("ERROR", format, args...) }

func Fatal(format string, args ...any) {
	output("FATAL", format, args...)
	os.Exit(1)
}

// Version is stamped at build time with -ldflags "-X tvt/logger.Version=...".
var Version = "dev"
