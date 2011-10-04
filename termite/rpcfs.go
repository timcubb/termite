package termite

import (
	"fmt"
	"os"
	"log"
	"rpc"
	"github.com/hanwen/go-fuse/fuse"
	"strings"
	"path/filepath"
)

type RpcFs struct {
	fuse.DefaultFileSystem
	cache *ContentCache

	client *rpc.Client

	// Roots that we should try to fetch locally.
	localRoots []string

	attr *AttributeCache
}

func NewRpcFs(server *rpc.Client, cache *ContentCache) *RpcFs {
	me := &RpcFs{}
	me.client = server

	me.attr = NewAttributeCache(
		func(n string) *FileAttr {
			return me.fetchAttr(n)
		}, nil)

	me.cache = cache
	return me
}

func (me *RpcFs) FetchHash(h string) {
	err := FetchBetweenContentServers(me.client, "FsServer.FileContent", h,
		me.cache)
	if err != nil {
		// TODO - drop master connection instead.
		log.Fatal("Error fetching contents: ", err)
	}
}

func (me *RpcFs) Update(req *UpdateRequest, resp *UpdateResponse) os.Error {
	me.updateFiles(req.Files)
	return nil
}

func (me *RpcFs) updateFiles(files []*FileAttr) {
	me.attr.Update(files)
}
	
func (me *RpcFs) fetchAttr(n string) *FileAttr {
	req := &AttrRequest{Name: n}
	rep := &AttrResponse{}
	err := me.client.Call("FsServer.GetAttr", req, rep)
	if err != nil {
		// fatal?
		log.Println("GetAttr error:", err)
		return nil
	}

	var wanted *FileAttr
	for _, attr := range rep.Attrs {
		if attr.Path == n {
			wanted = attr
		}
	}

	// TODO - if we got a deletion, we should refetch the parent.
	return wanted
}

func (me *RpcFs) considerSaveLocal(attr *FileAttr) {
	absPath := attr.Path
	if attr.Deletion() || !attr.FileInfo.IsRegular() {
		return
	}
	found := false
	for _, root := range me.localRoots {
		if HasDirPrefix(absPath, root) {
			found = true
		}
	}
	if !found {
		return
	}

	fi, _ := os.Lstat(absPath)
	if fi == nil {
		return
	}
	if EncodeFileInfo(*fi) != EncodeFileInfo(*attr.FileInfo) {
		return
	}

	// Avoid fetching local data; this assumes that most paths
	// will be the same between master and worker.  We mimick
	// fsserver's logic, so that we don't have nasty surprises
	// when running server and master on the same machine.
	if HasDirPrefix(absPath, "/usr") && !HasDirPrefix(absPath, "/usr/local") {
		me.cache.SaveImmutablePath(absPath)
	}
}

////////////////////////////////////////////////////////////////
// FS API

func (me *RpcFs) String() string {
	return "RpcFs"
}

func (me *RpcFs) OpenDir(name string, context *fuse.Context) (chan fuse.DirEntry, fuse.Status) {
	r := me.attr.Get(name)
	if r.Deletion() {
		return nil, fuse.ENOENT
	}
	if !r.FileInfo.IsDirectory() {
		return nil, fuse.EINVAL
	}

	c := make(chan fuse.DirEntry, len(r.NameModeMap))
	for k, mode := range r.NameModeMap {
		c <- fuse.DirEntry{
			Name: k,
			Mode: mode,
		}
	}
	close(c)
	return c, fuse.OK
}

type rpcFsFile struct {
	fuse.File
	os.FileInfo
}

func (me *rpcFsFile) GetAttr() (*os.FileInfo, fuse.Status) {
	return &me.FileInfo, fuse.OK
}

func (me *rpcFsFile) String() string {
	return fmt.Sprintf("rpcFsFile(%s)", me.File.String())
}

func (me *RpcFs) Open(name string, flags uint32, context *fuse.Context) (fuse.File, fuse.Status) {
	if flags&fuse.O_ANYWRITE != 0 {
		return nil, fuse.EPERM
	}
	a := me.attr.Get(name)
	if a == nil {
		return nil, fuse.ENOENT
	}
	if a.Deletion() {
		return nil, fuse.ENOENT
	}

	if contents := me.cache.ContentsIfLoaded(a.Hash); contents != nil {
		return &fuse.WithFlags{
			File: &rpcFsFile{
				fuse.NewDataFile(contents),
				*a.FileInfo,
			},
			FuseFlags: fuse.FOPEN_KEEP_CACHE,
		}, fuse.OK
	}

	p := me.cache.Path(a.Hash)
	if _, err := os.Lstat(p); fuse.OsErrorToErrno(err) == fuse.ENOENT {
		log.Printf("Fetching contents for file %s: %x", name, a.Hash)
		me.FetchHash(a.Hash)
	}

	f, err := os.Open(p)
	if err != nil {
		return nil, fuse.OsErrorToErrno(err)
	}

	return &fuse.WithFlags{
		File: &rpcFsFile{
			&fuse.ReadOnlyFile{&fuse.LoopbackFile{File: f}},
			*a.FileInfo,
		},
		FuseFlags: fuse.FOPEN_KEEP_CACHE,
	}, fuse.OK
}

func (me *RpcFs) Readlink(name string, context *fuse.Context) (string, fuse.Status) {
	a := me.attr.Get(name)
	if a == nil {
		return "", fuse.ENOENT
	}

	if a.Deletion() {
		return "", fuse.ENOENT
	}
	if !a.FileInfo.IsSymlink() {
		return "", fuse.EINVAL
	}

	return a.Link, fuse.OK
}

func (me *RpcFs) GetAttr(name string, context *fuse.Context) (*os.FileInfo, fuse.Status) {
	if name == "" {
		return &os.FileInfo{
			Mode: fuse.S_IFDIR | 0755,
		}, fuse.OK
	}

	dir, base := filepath.Split(name)
	dir = strings.TrimRight(dir, "/")
	if name != dir {
		dirResp := me.attr.Get(dir)
		found := dirResp != nil && dirResp.NameModeMap != nil &&
			dirResp.NameModeMap[base] != 0
		if !found {
			return nil, fuse.ENOENT
		}
	}
	
	r := me.attr.Get(name)
	if r == nil {
		return nil, fuse.ENOENT
	}
	if r.Hash != "" {
		me.FetchHash(r.Hash)
	}
	return r.FileInfo, r.Status()
}

func (me *RpcFs) Access(name string, mode uint32, context *fuse.Context) (code fuse.Status) {
	if mode == fuse.F_OK {
		_, code := me.GetAttr(name, context)
		return code
	}
	if mode&fuse.W_OK != 0 {
		return fuse.EACCES
	}
	return fuse.OK
}
