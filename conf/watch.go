package conf

import (
	"fmt"
	"github.com/fsnotify/fsnotify"
	"io"
	"log"
	"path/filepath"
	"time"
)

// Watch serializes candidate loads. The running configuration is committed only
// after apply succeeds; parsing or apply errors leave it untouched.
func (p *Conf) Watch(filePath, xDnsPath, sDnsPath string, apply func(*Conf) error) error {
	_, err := p.watch(filePath, xDnsPath, sDnsPath, apply, 500*time.Millisecond)
	return err
}
func (p *Conf) watch(filePath, xDnsPath, sDnsPath string, apply func(*Conf) error, delay time.Duration) (io.Closer, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	parents := map[string]bool{}
	for _, name := range []string{filePath, xDnsPath, sDnsPath} {
		if name == "" {
			continue
		}
		abs, e := filepath.Abs(name)
		if e != nil {
			watcher.Close()
			return nil, e
		}
		targets[abs] = true
		parents[filepath.Dir(abs)] = true
	}
	for parent := range parents {
		if err = watcher.Add(parent); err != nil {
			watcher.Close()
			return nil, fmt.Errorf("watch config directory failed")
		}
	}
	go func() {
		var timer *time.Timer
		var ready <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				abs, _ := filepath.Abs(event.Name)
				if !targets[abs] || event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				if timer != nil && !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				if timer == nil {
					timer = time.NewTimer(delay)
				} else {
					timer.Reset(delay)
				}
				ready = timer.C
			case <-ready:
				ready = nil
				candidate := New()
				if err := candidate.LoadFromPath(filePath); err != nil {
					log.Print("configuration reload rejected: invalid candidate")
					continue
				}
				if err := candidate.Validate(); err != nil {
					log.Print("configuration reload rejected: candidate validation failed")
					continue
				}
				if err := apply(candidate); err != nil {
					log.Print("configuration reload failed; last committed configuration retained")
					continue
				}
				*p = *candidate
				log.Print("reload config success")
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Print("configuration watcher error")
			}
		}
	}()
	return watcher, nil
}
