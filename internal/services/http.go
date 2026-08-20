package services

import (
	"fmt"
	"path"
	"strings"

	"github.com/yanchenko-igor/virtnet/internal/netstack/ipv4"
)

// FS is the filesystem interface needed by HTTP service.
type FS interface {
	ReadFile(path string) ([]byte, error)
}

type HTTPService struct {
	documentRoot string
	fs           FS
}

func init() {
	Register("http", NewHTTPService)
}

func NewHTTPService(config map[string]interface{}) Service {
	svc := &HTTPService{
		documentRoot: "/var/www",
	}
	if root, ok := config["document_root"].(string); ok {
		svc.documentRoot = root
	}
	return svc
}

func (h *HTTPService) SetFS(fs FS) {
	h.fs = fs
}

func (h *HTTPService) Ports() []ServicePort {
	return []ServicePort{
		{Port: 80, Proto: uint8(ipv4.ProtoTCP)},
	}
}

func (h *HTTPService) HandleRequest(ctx ServiceContext, req ServiceRequest) ([]byte, error) {
	if h.fs == nil {
		return []byte("HTTP/1.1 500 Internal Server Error\r\n\r\n"), nil
	}

	// Parse HTTP request
	requestStr := string(req.Payload)
	lines := strings.Split(requestStr, "\r\n")
	if len(lines) == 0 {
		return []byte("HTTP/1.1 400 Bad Request\r\n\r\n"), nil
	}

	// Parse request line: GET /path HTTP/1.1
	parts := strings.Split(lines[0], " ")
	if len(parts) != 3 {
		return []byte("HTTP/1.1 400 Bad Request\r\n\r\n"), nil
	}
	method, reqPath, version := parts[0], parts[1], parts[2]

	if method != "GET" && method != "HEAD" {
		return []byte("HTTP/1.1 501 Not Implemented\r\n\r\n"), nil
	}

	// Parse headers
	headers := make(map[string]string)
	for i := 1; i < len(lines); i++ {
		if lines[i] == "" {
			break
		}
		colon := strings.Index(lines[i], ":")
		if colon > 0 {
			key := strings.TrimSpace(lines[i][:colon])
			val := strings.TrimSpace(lines[i][colon+1:])
			headers[key] = val
		}
	}

	// Sanitize path
	if reqPath == "/" {
		reqPath = "/index.html"
	}
	reqPath = path.Clean(reqPath)
	if strings.HasPrefix(reqPath, "..") {
		return h.errorResponse(403, "Forbidden"), nil
	}

	// Build file path
	filePath := h.documentRoot + reqPath

	// Try to read file
	fileData, err := h.fs.ReadFile(filePath)
	if err != nil {
		// Try index.html for directories
		if reqPath == "/" || strings.HasSuffix(reqPath, "/") {
			indexPath := h.documentRoot + reqPath + "index.html"
			fileData, err = h.fs.ReadFile(indexPath)
			if err != nil {
				return h.errorResponse(404, fmt.Sprintf("Not Found: %s (tried %s)", reqPath, filePath)), nil
			}
			reqPath += "index.html"
		} else {
			return h.errorResponse(404, fmt.Sprintf("Not Found: %s (tried %s)", reqPath, filePath)), nil
		}
	}

	contentType := mimeTypeFromPath(reqPath)

	var response strings.Builder
	response.WriteString(fmt.Sprintf("%s 200 OK\r\n", version))
	response.WriteString(fmt.Sprintf("Content-Type: %s\r\n", contentType))
	response.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(fileData)))
	response.WriteString("Connection: close\r\n")
	response.WriteString("\r\n")

	body := response.String()
	if method == "HEAD" {
		return []byte(body), nil
	}
	return []byte(body + string(fileData)), nil
}

func (h *HTTPService) errorResponse(code int, message string) []byte {
	body := fmt.Sprintf("<html><body><h1>%d %s</h1></body></html>", code, message)
	return []byte(fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/html\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, message, len(body), body))
}

func mimeTypeFromPath(p string) string {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
