package acp

import "os"

// FSReadParams are the parameters for fs/read_text_file.
type FSReadParams struct {
	Path string `json:"path"`
}

// FSReadResult is the result of fs/read_text_file.
type FSReadResult struct {
	Content string `json:"content"`
}

// FSWriteParams are the parameters for fs/write_text_file.
type FSWriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// FSWriteResult is the result of fs/write_text_file.
type FSWriteResult struct {
	BytesWritten int `json:"bytesWritten"`
}

// ReadFile reads a file from the sandbox filesystem.
func ReadFile(params FSReadParams) (*FSReadResult, error) {
	data, err := os.ReadFile(params.Path)
	if err != nil {
		return nil, err
	}
	return &FSReadResult{Content: string(data)}, nil
}

// WriteFile writes a file to the sandbox filesystem.
func WriteFile(params FSWriteParams) (*FSWriteResult, error) {
	data := []byte(params.Content)
	if err := os.WriteFile(params.Path, data, 0644); err != nil {
		return nil, err
	}
	return &FSWriteResult{BytesWritten: len(data)}, nil
}
