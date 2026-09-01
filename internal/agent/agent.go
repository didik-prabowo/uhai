// Package agent berisi loop agentic utama: kirim pesan ke provider, kalau
// provider minta panggil tool maka eksekusi, kirim hasilnya balik, ulangi.
// Package ini cuma bergantung pada interface provider.Provider — gak peduli
// implementasi di baliknya Anthropic, OpenAI, atau vendor lain.
package agent

import (
	"fmt"

	"github.com/didik-prabowo/ouhai/internal/provider"
	"github.com/didik-prabowo/ouhai/internal/tools"
)

const DefaultSystemPrompt = `Kamu adalah ouhai, CLI coding agent yang jalan di terminal user.
Kamu bantu user ngerjain tugas software engineering.

# Harness
- Output kamu ditampilkan langsung di terminal sebagai teks biasa.
- Tool yang tersedia: read_file (baca file), write_file (tulis/timpa file,
  folder induk dibuat otomatis), run_bash (jalanin satu perintah shell,
  balikin stdout+stderr).
- Baca file dulu sebelum mengubahnya. write_file menimpa seluruh isi file,
  jadi jangan pakai kalau kamu belum tahu isi lamanya.
- Pakai run_bash buat cari file (grep, find, ls) dan buat verifikasi
  (build, test). Sebut path file sebagai path/ke/file.go:12 supaya bisa diklik.
- Panggil beberapa tool yang gak saling bergantung sekaligus kalau bisa.

# Cara kerja
- Kerjakan apa yang diminta, gak lebih dan gak kurang. Jangan diam-diam
  memperluas atau menyempitkan scope.
- Ambigu ringan: putuskan sendiri seperti rekan kerja yang teliti, sebutkan
  asumsinya. Tanya cuma kalau dua tafsiran menghasilkan kerjaan yang beda jauh.
- Jangan bikin file baru kalau bisa nempel di file yang sudah ada. Jangan
  bikin README atau dokumentasi kecuali diminta.
- Ikuti gaya kode yang sudah ada di repo (penamaan, komentar, idiom).
- Verifikasi hasil kerja kalau ada caranya (build/test), lalu laporkan apa
  adanya. Kalau test gagal, bilang gagal beserta outputnya. Kalau ada bagian
  yang di-skip, bilang.
- Aksi yang susah dibalik (hapus file, git push, kirim ke layanan luar):
  konfirmasi dulu ke user kecuali sudah jelas disuruh.

# Gaya output
- Ringkas. Jawab langsung ke intinya, tanpa basa-basi pembuka atau penutup.
- Jelaskan singkat apa yang mau kamu lakukan sebelum manggil tool.
- Jangan tempel ulang isi file yang panjang, cukup sebut path + baris.
- Kalau tugas selesai, kasih ringkasan singkat tanpa manggil tool lagi.`

const defaultMaxIterations = 25

// Agent membungkus satu Provider + history percakapan + hook buat observasi.
type Agent struct {
	Provider      provider.Provider
	System        string
	MaxIterations int
	History       []provider.Message

	// OnText dipanggil tiap kali ada blok teks dari model, buat ditampilkan
	// ke user. OnToolCall dipanggil sebelum tool dieksekusi.
	OnText     func(text string)
	OnToolCall func(name string, input string)

	// Confirm dipanggil buat tool yang tools.NeedsConfirm() — kalau balikin
	// false, tool gak dijalankan dan model dikasih tahu user menolak.
	// Default-nya nolak semua: pemanggil yang gak pasang hook ini gak akan
	// diam-diam nulis file atau jalanin perintah.
	Confirm func(name string, input string) bool
}

func New(p provider.Provider) *Agent {
	return &Agent{
		Provider:      p,
		System:        DefaultSystemPrompt,
		MaxIterations: defaultMaxIterations,
		OnText:        func(string) {},
		OnToolCall:    func(string, string) {},
		Confirm:       func(string, string) bool { return false },
	}
}

// Ask menambahkan prompt user ke history, lalu menjalankan loop agentic
// sampai model selesai (stop_reason != tool_use) atau limit iterasi tercapai.
func (a *Agent) Ask(userPrompt string) error {
	a.History = append(a.History, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: userPrompt}},
	})

	for i := 0; i < a.MaxIterations; i++ {
		resp, err := a.Provider.Send(provider.Request{
			System:   a.System,
			Messages: a.History,
			Tools:    tools.Definitions(),
		})
		if err != nil {
			return fmt.Errorf("provider error: %w", err)
		}

		for _, block := range resp.Content {
			if block.Type == provider.BlockText && block.Text != "" {
				a.OnText(block.Text)
			}
		}

		a.History = append(a.History, provider.Message{
			Role:    provider.RoleAssistant,
			Content: resp.Content,
		})

		if resp.StopReason != provider.StopToolUse {
			return nil
		}

		toolResults := a.runTools(resp.Content)
		a.History = append(a.History, provider.Message{
			Role:    provider.RoleUser,
			Content: toolResults,
		})
	}

	return fmt.Errorf("berhenti: mencapai batas %d iterasi tanpa selesai", a.MaxIterations)
}

func (a *Agent) runTools(blocks []provider.ContentBlock) []provider.ContentBlock {
	var results []provider.ContentBlock
	for _, block := range blocks {
		if block.Type != provider.BlockToolUse {
			continue
		}

		result, isError := "", false
		if tools.NeedsConfirm(block.ToolName) && !a.Confirm(block.ToolName, string(block.ToolInput)) {
			result, isError = "user menolak menjalankan tool ini", true
		} else {
			a.OnToolCall(block.ToolName, string(block.ToolInput))
			result, isError = tools.Execute(block.ToolName, block.ToolInput)
		}

		results = append(results, provider.ContentBlock{
			Type:            provider.BlockToolResult,
			ToolResultForID: block.ToolUseID,
			ToolResultText:  result,
			ToolResultError: isError,
		})
	}
	return results
}
