package fs

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
)

var _ = log.Println

// Read files from proc - since they have 0 size, we must read the
// file to set the size correctly.
type ProcFs struct {
	pathfs.FileSystem
	StripPrefix      string
	AllowedRootFiles map[string]int
	Uid              int
}

func NewProcFs() *ProcFs {
	return &ProcFs{
		FileSystem:  pathfs.NewLoopbackFileSystem("/proc"),
		StripPrefix: "/",
		AllowedRootFiles: map[string]int{
			"meminfo":     1,
			"cpuinfo":     1,
			"iomem":       1,
			"ioport":      1,
			"loadavg":     1,
			"stat":        1,
			"self":        1,
			"filesystems": 1,
			"mounts":      1,
			"version":     1,
		},
	}
}

func isNum(n string) bool {
	for _, c := range n {
		if !unicode.IsDigit(c) {
			return false
		}
	}
	return len(n) > 0
}

// TODO - move into fuse
func SplitPath(name string) (dir, base string) {
	dir, base = filepath.Split(name)
	dir = strings.TrimRight(dir, "/")
	return dir, base
}

func (me *ProcFs) GetAttr(name string, context *fuse.Context) (*fuse.Attr, fuse.Status) {
	dir, base := SplitPath(name)
	if name != "" && dir == "" && !isNum(name) && me.AllowedRootFiles != nil {
		if _, ok := me.AllowedRootFiles[base]; !ok {
			return nil, fuse.ENOENT
		}
	}

	fi, code := me.FileSystem.GetAttr(name, context)
	if code.Ok() && isNum(dir) && os.Geteuid() == 0 && uint32(fi.Uid) != context.Uid {
		return nil, fuse.EPERM
	}
	if fi != nil && fi.IsRegular() && fi.Size == 0 {
		p := filepath.Join("/proc", name)
		content, _ := ioutil.ReadFile(p)
		fi.Size = uint64(len(content))
	}
	return fi, code
}

func (me *ProcFs) Open(name string, flags uint32, context *fuse.Context) (nodefs.File, fuse.Status) {
	p := filepath.Join("/proc", name)
	content, err := ioutil.ReadFile(p)
	if err == nil {
		return nodefs.NewDataFile(content), fuse.OK
	}
	return nil, fuse.ToStatus(err)
}

func (me *ProcFs) String() string {
	return "ProcFs"
}

func (me *ProcFs) Readlink(name string, context *fuse.Context) (string, fuse.Status) {
	if name == "self" {
		return fmt.Sprintf("%d", context.Pid), fuse.OK
	}
	val, code := me.FileSystem.Readlink(name, context)
	if code.Ok() && strings.HasPrefix(val, me.StripPrefix) {
		val = "/" + strings.TrimLeft(val[len(me.StripPrefix):], "/")
	}
	return val, code
}
