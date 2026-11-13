package federation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"tvt/logger"
)

const (
	MinInstanceName = 2
	MaxInstanceName = 24
)

var ErrInstanceName = errors.New("instance name")

// CleanInstanceName trims and collapses spaces, then enforces the rules for a
// server's public name: 2 to 24 characters, letters or digits of any script plus
// spaces and . , ' & + ! ( ) - _, starting and ending with a letter or digit.
func CleanInstanceName(in string) (string, error) {
	name := strings.Join(strings.Fields(in), " ")
	n := utf8.RuneCountInString(name)
	if n < MinInstanceName || n > MaxInstanceName {
		return "", fmt.Errorf("%w: must be %d to %d characters", ErrInstanceName, MinInstanceName, MaxInstanceName)
	}
	runes := []rune(name)
	for _, r := range runes {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || strings.ContainsRune(" .,'&+!()-_", r)) {
			return "", fmt.Errorf("%w: %q is not allowed", ErrInstanceName, string(r))
		}
	}
	if !(unicode.IsLetter(runes[0]) || unicode.IsDigit(runes[0])) || !(unicode.IsLetter(runes[n-1]) || unicode.IsDigit(runes[n-1]) || runes[n-1] == ')' || runes[n-1] == '!') {
		return "", fmt.Errorf("%w: must start and end with a letter or digit", ErrInstanceName)
	}
	return name, nil
}

func (s *Service) ownName() string {
	if s.Name == nil {
		return ""
	}
	name, err := CleanInstanceName(s.Name())
	if err != nil {
		logger.Warn("FEDERATION own instance name is not announced: %v", err)
		return ""
	}
	return name
}

// SendInstanceName tells every known server the name this one now goes by.
func (s *Service) SendInstanceName() error {
	name := s.ownName()
	if name == "" {
		return fmt.Errorf("%w: nothing valid to announce", ErrInstanceName)
	}
	hosts, err := s.Store.PeerHosts()
	if err != nil {
		return fmt.Errorf("list servers to tell about the new name: %w", err)
	}
	for _, h := range hosts {
		s.enqueue(h, message{Type: "instance", Instance: name})
	}
	logger.Info("FEDERATION announced instance name %q to %d servers", name, len(hosts))
	return nil
}

func (s *Service) onInstance(host string, m message) error {
	name, err := CleanInstanceName(m.Instance)
	if err != nil {
		return errBadMessage
	}
	if err := s.Store.SetPeerName(host, name); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.refreshProfile(ctx, host)
	}()
	return nil
}
