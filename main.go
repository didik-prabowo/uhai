// Command ouhai adalah CLI coding agent minimal. Sekarang isinya baru
// shell terminal-nya: kotak welcome, form input, dan slash command.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/didik-prabowo/ouhai/internal/agent"
	"github.com/didik-prabowo/ouhai/internal/config"
)

const (
	dim   = "\x1b[2m"
	cyan  = "\x1b[36m"
	white = "\x1b[97m"
	reset = "\x1b[0m"
)

// command adalah satu slash command yang muncul di menu waktu user ngetik "/".
type command struct {
	name string
	desc string
}

var commands = []command{
	{"/help", "tampilkan daftar perintah"},
	{"/clear", "bersihkan layar"},
	{"/exit", "keluar dari ouhai"},
}

// matches mengembalikan command yang namanya diawali input. Menu cuma muncul
// kalau input diawali "/" — teks biasa gak perlu diganggu.
func matches(input string) []command {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	var out []command
	for _, c := range commands {
		if strings.HasPrefix(c.name, input) {
			out = append(out, c)
		}
	}
	return out
}

// termWidth mengembalikan lebar terminal sekarang, biar garisnya full lebar
// dan ikut menyesuaikan kalau window di-resize. Fallback 80 kalau stdout
// bukan tty (misalnya output di-pipe).
func termWidth() int {
	var ws struct{ row, col, x, y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.col < 20 {
		return 80
	}
	return int(ws.col)
}

// box mencetak kotak welcome ala Claude Code: lebarnya ikut baris terpanjang,
// dipotong kalau terminalnya lebih sempit.
func box(lines ...string) {
	w := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > w {
			w = n
		}
	}
	if max := termWidth() - 4; w > max {
		w = max
	}

	border := strings.Repeat("─", w+2)
	fmt.Printf("%s╭%s╮%s\n", dim, border, reset)
	for _, l := range lines {
		// Padding dihitung per rune, bukan per byte, dan warnanya dipasang
		// setelah padding — biar border kanan gak melenceng.
		r := []rune(l)
		if len(r) > w {
			r = r[:w]
		}
		text := strings.Replace(string(r), "✻", cyan+"✻"+reset, 1)
		fmt.Printf("%s│%s %s%s %s│%s\n", dim, reset, text, strings.Repeat(" ", w-len(r)), dim, reset)
	}
	fmt.Printf("%s╰%s╯%s\n", dim, border, reset)
}

// truncate memotong teks panjang biar prompt konfirmasi gak kebanjiran isi file.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// rule mencetak garis pemisah selebar terminal.
func rule() {
	fmt.Printf("%s%s%s\n", dim, strings.Repeat("─", termWidth()), reset)
}

// frame merakit bagian yang digambar ulang tiap tombol: baris input, menu
// slash command, dan garis bawah. Garis atas sengaja gak ikut — kalau ikut,
// tiap redraw bakal numpuk form baru. up = berapa baris kursor harus naik
// buat balik ke baris input; kalau kurang, kursor nyangkut di garis bawah dan
// form-nya nambah tiap tombol.
func frame(input string, list []command, sel, w int) (out string, up int) {
	line := dim + strings.Repeat("─", w) + reset

	var b strings.Builder
	b.WriteString("\r\x1b[J") // hapus dari baris input ke bawah
	// Prefix putih, teks yang diketik pakai warna default terminal — sama
	// seperti Claude Code, teksnya gak ditint.
	fmt.Fprintf(&b, "%s❯%s %s\n", white, reset, input)
	for i, c := range list {
		if i == sel {
			// Yang terpilih putih terang, sisanya dim — tanpa penanda "❯"
			// biar gak ketuker sama prefix input.
			fmt.Fprintf(&b, "%s  %-10s %s%s\n", white, c.name, c.desc, reset)
			continue
		}
		fmt.Fprintf(&b, "%s  %-10s %s%s\n", dim, c.name, c.desc, reset)
	}
	b.WriteString(line + "\n")

	// Setelah blok ini dicetak, kursor ada satu baris di bawah garis bawah:
	// baris input + menu + garis bawah + 1.
	return b.String(), len(list) + 2
}

// rawMode matiin echo & line buffering terminal supaya tiap tombol langsung
// kebaca — perlu buat nampilin menu begitu "/" diketik. Balikin fungsi restore
// dan ok=false kalau stdin bukan tty (input di-pipe).
func rawMode() (restore func(), ok bool) {
	fd := os.Stdin.Fd()
	ioctl := func(req uintptr, t *syscall.Termios) bool {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t)))
		return errno == 0
	}

	var old syscall.Termios
	if !ioctl(syscall.TIOCGETA, &old) {
		return func() {}, false
	}

	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Iflag &^= syscall.ICRNL | syscall.IXON
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if !ioctl(syscall.TIOCSETA, &raw) {
		return func() {}, false
	}
	return func() { ioctl(syscall.TIOCSETA, &old) }, true
}

// readLine menggambar form input dan membaca satu baris, sambil nampilin menu
// slash command tiap kali isinya diawali "/". Balikin ok=false kalau EOF/Ctrl+D.
//
// ponytail: cuma append & backspace — belum ada history, panah kiri/kanan,
// atau multiline. Pindah ke bubbletea kalau butuh itu.
func readLine(in *bufio.Reader) (string, bool) {
	var buf []rune
	sel := 0 // baris menu yang lagi disorot

	rule() // garis atas digambar sekali, bukan tiap tombol

	draw := func() {
		list := matches(string(buf))
		if sel >= len(list) {
			sel = len(list) - 1
		}
		if sel < 0 {
			sel = 0
		}
		out, up := frame(string(buf), list, sel, termWidth())
		fmt.Print(out)
		fmt.Printf("\x1b[%dA\x1b[%dG", up, len(buf)+3)
	}

	draw()
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return "", false
		}

		switch r {
		case '\r', '\n':
			// Gambar ulang tanpa menu, biar yang ketinggal di scrollback cuma
			// form input + isinya.
			if list := matches(string(buf)); len(list) > 0 {
				buf = []rune(list[sel].name)
			}
			out, _ := frame(string(buf), nil, 0, termWidth())
			fmt.Print(out)
			return strings.TrimSpace(string(buf)), true
		case 127, 8: // backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				sel = 0
			}
		case '\t': // lengkapi ke kandidat pertama
			if list := matches(string(buf)); len(list) > 0 {
				buf = []rune(list[sel].name)
			}
		case 3: // Ctrl+C: kosongin baris, sekali lagi kalau sudah kosong = keluar
			if len(buf) == 0 {
				fmt.Print("\x1b[J")
				return "/exit", true
			}
			buf = buf[:0]
		case 4: // Ctrl+D
			fmt.Print("\x1b[J")
			return "", false
		case 27: // escape sequence: panah atas/bawah buat pindah sorotan
			if r, _, err := in.ReadRune(); err != nil || r != '[' {
				continue
			}
			r, _, err := in.ReadRune()
			if err != nil {
				continue
			}
			switch r {
			case 'A':
				sel--
			case 'B':
				sel++
			}
		default:
			if r >= 32 {
				buf = append(buf, r)
				sel = 0
			}
		}
		draw()
	}
}

// key baca satu tombol jawaban konfirmasi. Di raw mode cukup sekali pencet,
// tanpa Enter; kalau stdin bukan tty, baca satu baris seperti biasa.
func key(in *bufio.Reader, raw bool) rune {
	if !raw {
		line, err := in.ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "" {
			return 'n'
		}
		return rune(strings.ToLower(strings.TrimSpace(line))[0])
	}
	r, _, err := in.ReadRune()
	if err != nil {
		return 'n'
	}
	return unicode.ToLower(r)
}

// run menjalankan satu baris input: slash command, atau prompt buat agent.
// Balikin false kalau harus keluar.
func run(a *agent.Agent, line string) bool {
	switch line {
	case "/exit", "/quit", "exit", "quit":
		return false
	case "/help":
		for _, c := range commands {
			fmt.Printf("  %s%-10s%s %s\n", cyan, c.name, reset, c.desc)
		}
	case "/clear":
		fmt.Print("\x1b[2J\x1b[H")
		a.History = nil // layar bersih, konteks ikut bersih
	default:
		if strings.HasPrefix(line, "/") {
			fmt.Printf("%s  perintah gak dikenal: %s (coba /help)%s\n", dim, line, reset)
			break
		}
		if err := a.Ask(line); err != nil {
			fmt.Printf("%s  error: %v%s\n", dim, err, reset)
		}
	}
	fmt.Println()
	return true
}

func main() {
	// Provider dirakit sebelum terminal disetel raw — kalau gagal, kita keluar
	// tanpa ninggalin terminal dalam keadaan aneh.
	p, err := config.LoadProvider()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	restore, raw := rawMode()
	defer restore()
	in := bufio.NewReader(os.Stdin)

	a := agent.New(p)
	a.OnText = func(text string) {
		fmt.Printf("%s⏺%s %s\n", cyan, reset, text)
	}
	a.OnToolCall = func(name, input string) {
		fmt.Printf("%s  ⎿ %s(%s)%s\n", dim, name, truncate(input, 200), reset)
	}

	// Tool yang nulis file / jalanin perintah minta izin dulu. "a" nyimpen
	// izin buat sisa sesi, per nama tool.
	allowAll := map[string]bool{}
	a.Confirm = func(name, input string) bool {
		if allowAll[name] {
			return true
		}
		fmt.Printf("%s⏺%s %s(%s)\n", cyan, reset, name, truncate(input, 400))
		fmt.Printf("%s  jalanin? [y] ya  [a] ya, & jangan tanya lagi  [n] tolak%s ", dim, reset)

		switch key(in, raw) {
		case 'y':
			fmt.Print("y\n")
			return true
		case 'a':
			fmt.Print("a\n")
			allowAll[name] = true
			return true
		default:
			fmt.Printf("n%s — ditolak%s\n", dim, reset)
			return false
		}
	}

	cwd, _ := os.Getwd()
	box(
		"✻ Welcome to ouhai!",
		"",
		"  provider: "+p.Name(),
		"  ketik / buat lihat perintah, /exit buat keluar",
		"",
		"  cwd: "+cwd,
	)
	fmt.Println()

	for {
		var line string
		var ok bool
		if raw {
			line, ok = readLine(in)
		} else {
			// stdin bukan tty: baca biasa, tanpa menu.
			var s string
			s, err := in.ReadString('\n')
			line, ok = strings.TrimSpace(s), err == nil
		}
		if !ok {
			return
		}
		if line == "" {
			continue
		}
		if !run(a, line) {
			return
		}
	}
}
