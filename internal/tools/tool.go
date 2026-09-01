// Package tools berisi definisi tool yang bisa dipanggil model (read_file,
// write_file, run_bash) beserta eksekusinya. Package ini gak tahu apa-apa
// soal provider mana yang dipakai — tool bekerja sama persis buat Anthropic,
// OpenAI, atau provider lain apa pun.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

const bashTimeout = 30 * time.Second
const maxResultLen = 8000

// Definitions mengembalikan daftar ToolSpec yang dikirim ke provider
// supaya model tahu tool apa saja yang tersedia.
func Definitions() []provider.ToolSpec {
	return []provider.ToolSpec{
		{
			Name:        "read_file",
			Description: "Baca isi sebuah file di disk. Kembalikan seluruh isi file sebagai teks.",
			JSONSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path relatif atau absolut ke file"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "write_file",
			Description: "Tulis (timpa) konten ke sebuah file. Buat file baru kalau belum ada, termasuk folder induknya.",
			JSONSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path file yang mau ditulis"},
					"content": {"type": "string", "description": "Konten lengkap yang mau ditulis ke file"}
				},
				"required": ["path", "content"]
			}`),
		},
		{
			Name:        "run_bash",
			Description: "Jalankan satu perintah shell (bash) dan kembalikan stdout+stderr-nya.",
			JSONSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Perintah shell yang mau dijalankan"}
				},
				"required": ["command"]
			}`),
		},
	}
}

// NeedsConfirm menandai tool yang efeknya keluar dari proses ini (nulis ke
// disk, jalanin perintah) — pemanggilnya wajib minta izin user dulu.
func NeedsConfirm(name string) bool {
	return name == "write_file" || name == "run_bash"
}

// Execute menjalankan satu tool berdasarkan nama, dan mengembalikan hasilnya
// sebagai teks (sudah dipotong kalau kepanjangan) beserta flag error.
func Execute(name string, input json.RawMessage) (result string, isError bool) {
	switch name {
	case "read_file":
		result, isError = readFile(input)
	case "write_file":
		result, isError = writeFile(input)
	case "run_bash":
		result, isError = runBash(input)
	default:
		return fmt.Sprintf("tool tidak dikenal: %s", name), true
	}

	if len(result) > maxResultLen {
		result = result[:maxResultLen] + "\n...[output dipotong]"
	}
	return result, isError
}

func readFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return fmt.Sprintf("gagal baca file: %v", err), true
	}
	return string(data), false
}

func writeFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		return fmt.Sprintf("gagal tulis file: %v", err), true
	}
	return fmt.Sprintf("OK, %d bytes ditulis ke %s", len(args.Content), args.Path), false
}

func runBash(input json.RawMessage) (string, bool) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}

	cmd := exec.Command("bash", "-c", args.Command)
	type execResult struct {
		out []byte
		err error
	}
	done := make(chan execResult, 1)

	go func() {
		out, err := cmd.CombinedOutput()
		done <- execResult{out, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return fmt.Sprintf("exit error: %v\noutput:\n%s", res.err, res.out), true
		}
		return string(res.out), false
	case <-time.After(bashTimeout):
		_ = cmd.Process.Kill()
		return "timeout: command melebihi batas waktu", true
	}
}
