package dispatcher

import (
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type UploadTrafficWriter struct {
	Counter *counter.TrafficStorage
	Writer  buf.Writer
}

type DownloadTrafficWriter struct {
	Counter *counter.TrafficStorage
	Writer  buf.Writer
}

func (w *UploadTrafficWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.Counter.UpCounter.Add(int64(mb.Len()))
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *UploadTrafficWriter) Close() error {
	return common.Close(w.Writer)
}

func (w *UploadTrafficWriter) Interrupt() {
	common.Interrupt(w.Writer)
}

func (w *DownloadTrafficWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.Counter.DownCounter.Add(int64(mb.Len()))
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *DownloadTrafficWriter) Close() error {
	return common.Close(w.Writer)
}

func (w *DownloadTrafficWriter) Interrupt() {
	common.Interrupt(w.Writer)
}
