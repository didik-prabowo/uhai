package main

import (
	"strings"
	"testing"

	"github.com/didik-prabowo/ouhai/internal/agent"
)

func TestMatches(t *testing.T) {
	if got := matches("halo"); got != nil {
		t.Fatalf("teks biasa gak boleh munculin menu: %v", got)
	}
	if got := matches("/"); len(got) != len(commands) {
		t.Fatalf("\"/\" harus munculin semua perintah, dapat %d", len(got))
	}
	if got := matches("/e"); len(got) != 1 || got[0].name != "/exit" {
		t.Fatalf("\"/e\" harusnya cuma /exit, dapat %v", got)
	}
	if got := matches("/zz"); got != nil {
		t.Fatalf("prefix gak cocok harus kosong, dapat %v", got)
	}
}

func TestRunExit(t *testing.T) {
	// Provider nil aman: /exit dan /help gak pernah nyentuh provider.
	a := agent.New(nil)
	for _, line := range []string{"/exit", "/quit", "exit"} {
		if run(a, line) {
			t.Fatalf("%q harusnya keluar", line)
		}
	}
	if !run(a, "/help") {
		t.Fatal("/help harusnya lanjut")
	}
}

func TestFrameCumaSatuGaris(t *testing.T) {
	// Tiap redraw cuma boleh nulis garis bawah. Kalau garis atas ikut kegambar,
	// form-nya numpuk tiap tombol diketik (bug "/sd munculin form terus").
	out, up := frame("/sd", nil, 0, 10)
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "─") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("harus 1 baris garis per redraw, dapat %d: %q", n, out)
	}
	// input + garis bawah + posisi kursor setelah cetak = naik 2.
	if up != 2 {
		t.Fatalf("tanpa menu kursor naik 2 baris, dapat %d", up)
	}

	// Dengan menu, kursor naik sebanyak baris menu + garis bawah.
	if _, up := frame("/e", matches("/e"), 0, 10); up != 3 {
		t.Fatalf("1 item menu harusnya naik 3, dapat %d", up)
	}
}

func TestFrameSorotBarisTerpilih(t *testing.T) {
	list := matches("/")
	out, _ := frame("/", list, 1, 10)
	// Baris pertama = input, yang dicek baris menunya.
	for _, l := range strings.Split(out, "\n")[1:] {
		if !strings.Contains(l, white) {
			continue
		}
		if !strings.Contains(l, list[1].name) {
			t.Fatalf("sorotan salah baris: %q", l)
		}
		return
	}
	t.Fatal("gak ada baris yang disorot")
}
