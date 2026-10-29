package limiter

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"regexp"
)

type ruleSnapshot struct {
	domains   []*regexp.Regexp
	protocols []string
}

func (l *Limiter) CheckDomainRule(destination string) bool {
	r := l.rules.Load()
	if r == nil {
		return false
	}
	for _, p := range r.domains {
		if p.MatchString(destination) {
			return true
		}
	}
	return false
}
func (l *Limiter) CheckProtocolRule(protocol string) bool {
	r := l.rules.Load()
	if r == nil {
		return false
	}
	for _, p := range r.protocols {
		if p == protocol {
			return true
		}
	}
	return false
}
func (l *Limiter) UpdateRule(rule *panel.Rules) error {
	next := &ruleSnapshot{}
	if rule != nil {
		next.protocols = append([]string(nil), rule.Protocol...)
		for i, p := range rule.Regexp {
			compiled, err := regexp.Compile(p)
			if err != nil {
				return fmt.Errorf("invalid domain rule at index %d", i)
			}
			next.domains = append(next.domains, compiled)
		}
	}
	l.rules.Store(next)
	return nil
}
