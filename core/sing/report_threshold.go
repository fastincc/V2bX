package sing

func (s *Sing) setReportMinTraffic(tag string, value int64) {
	s.reportMinMu.Lock()
	defer s.reportMinMu.Unlock()
	if s.nodeReportMinTrafficBytes == nil {
		s.nodeReportMinTrafficBytes = make(map[string]int64)
	}
	s.nodeReportMinTrafficBytes[tag] = value
}

func (s *Sing) reportMinTraffic(tag string) int64 {
	s.reportMinMu.RLock()
	defer s.reportMinMu.RUnlock()
	return s.nodeReportMinTrafficBytes[tag]
}
