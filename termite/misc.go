package termite

import (
	"crypto"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/fuse"
)

func init() {
	rand.Seed(time.Now().UnixNano() ^ (int64(os.Getpid()) << 32))
}

// TODO - move into fuse
func SplitPath(name string) (dir, base string) {
	dir, base = filepath.Split(name)
	dir = strings.TrimRight(dir, "/")
	return dir, base
}

func RandomBytes(n int) []byte {
	c := make([]byte, 0)
	for i := 0; i < n; i++ {
		c = append(c, byte(rand.Int31n(256)))
	}
	return c
}

func md5str(s string) string {
	h := crypto.MD5.New()
	io.WriteString(h, s)
	return string(h.Sum(nil))
}

func Version() string {
	tVersion := "unknown"
	if version != nil {
		tVersion = *version
	}

	return fmt.Sprintf("Termite %s (go-fuse %s)",
		tVersion, fuse.Version())
}

func EscapeRegexp(s string) string {
	special := "[]()\\+*"
	for i := range special {
		c := special[i : i+1]
		s = strings.Replace(s, c, "\\"+c, -1)
	}
	return s
}

func DetectFiles(root string, cmd string) []string {
	regexp, err := regexp.Compile("(" + EscapeRegexp(root) + "/[^ ;&|\"']*)")
	if err != nil {
		log.Println("regexp error", err)
	}

	names := regexp.FindAllString(cmd, -1)
	return names
}

func IsSpace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\f' || b == '\t'
}

var controlCharMap = map[byte]bool{
	'$': true,
	'>': true,
	'<': true,
	'&': true,
	'|': true,
	';': true,
	'*': true,
	'?': true,
	// TODO - [] function as wildcards, but let's slip this through
	// rather than patching up the LLVM compile.
	//	'[': true,
	//	']': true,
	'(': true,
	')': true,
	'{': true,
	'}': true,
	'~': true,
	'`': true,
	'#': true,
}

func MakeUnescape(cmd string) string {
	word := make([]byte, 0, len(cmd))

	lastSlash := false
	for _, intCh := range cmd {
		ch := byte(intCh)
		if lastSlash {
			if ch != '\n' {
				word = append(word, '\\', ch)
			}
			lastSlash = false
		} else {
			if ch == '\\' {
				lastSlash = true
			} else {
				word = append(word, ch)
			}
		}
	}

	return string(word)
}

// ParseCommand tries to parse quoting for a shell command line.  It
// will give up and return nil when it returns shell-metacharacters
// ($, ` , etc.)
func ParseCommand(cmd string) []string {
	escape := false
	squote := false
	dquote := false

	result := []string{}
	word := []byte{}
	for i, ch := range cmd {
		c := byte(ch)
		if squote {
			if c == '\'' {
				squote = false
			} else {
				word = append(word, c)
			}
			continue
		}
		if dquote {
			// TODO - not really correct; "a\nb" -> a\nb
			if escape {
				word = append(word, byte(c))
				escape = false
				continue
			}

			switch c {
			case '"':
				dquote = !dquote
			case '\\':
				escape = true
			case '$':
				return nil
			default:
				word = append(word, c)
			}
			continue
		}
		if escape {
			word = append(word, c)
			escape = false
			continue
		}
		if c == '\'' {
			squote = true
			continue
		}
		if c == '"' {
			dquote = true
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		if controlCharMap[c] {
			return nil
		}
		if IsSpace(c) {
			if i > 0 && !IsSpace(cmd[i-1]) {
				result = append(result, string(word))
				word = []byte{}
			}
		} else {
			word = append(word, c)
		}
	}

	if len(cmd) > 0 && !IsSpace(cmd[len(cmd)-1]) {
		result = append(result, string(word))
	}
	return result
}

func HasDirPrefix(path, prefix string) bool {
	prefix = strings.TrimRight(prefix, string(filepath.Separator))
	path = strings.TrimRight(path, string(filepath.Separator))
	return path == prefix ||
		strings.HasPrefix(path, prefix+string(filepath.Separator))
}

func HumanTrim(s string, l int) string {
	if len(s) < l {
		return s
	}
	trail := fmt.Sprintf(" ... (%d bytes)", len(s))
	return s[:l-len(trail)] + trail
}

func PrintStdinSliceLen(s []byte) {
	log.Printf("Copied %d bytes of stdin", len(s))
}

// Useful for debugging.
func HookedCopy(w io.Writer, r io.Reader, proc func([]byte)) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 && proc != nil {
			proc(buf[:n])
		}
		todo := buf[:n]
		for len(todo) > 0 {
			n, err = w.Write(todo)
			if err != nil {
				break
			}
			todo = todo[n:]
		}
		if len(todo) > 0 {
			return err
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (me *WorkResponse) String() string {
	return fmt.Sprintf("WorkResponse{exit %d, taskids %v: %v. Err: %s, Out: %s}",
		me.Exit.ExitStatus(),
		me.TaskIds,
		me.FileSet,
		HumanTrim(me.Stderr, 1024),
		HumanTrim(me.Stdout, 1024))
}

// IntToExponent the smallest E such that 2^E >= Z.
func IntToExponent(z int) uint {
	x := z
	var exp uint = 0
	for x > 1 {
		exp++
		x >>= 1
	}

	if z > (1 << exp) {
		exp++
	}
	return exp
}

