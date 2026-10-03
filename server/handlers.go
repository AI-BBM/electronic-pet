package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

var errBodyTooLarge = errors.New("request body too large")

// maxBodyBytes 各 POST 的合法请求体远小于该上限，防无界解码 DoS。
const maxBodyBytes = 64 << 10

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeJSON 严格解析请求体；超限返回 errBodyTooLarge（413）。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errBodyTooLarge
		}
		return errors.New("invalid JSON body")
	}
	return nil
}

// writeInternal 记录内部错误到服务端日志，向客户端只回笼统文案（不透传驱动细节）。
func writeInternal(w http.ResponseWriter, err error, context string) {
	log.Printf("[500] %s: %v", context, err)
	writeJSON(w, http.StatusInternalServerError, errJSON("服务器开小差了，请稍后再试"))
}
