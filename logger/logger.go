package logger

import (
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
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

func output(level, format string, args ...any) {
	std.Output(3, level+" "+redact(fmt.Sprintf(format, args...)))
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
