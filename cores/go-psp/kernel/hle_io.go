package kernel

import (
	"sort"
	"strings"

	"github.com/davidpatton/itak-gaming/cores/go-psp/umd"
)

// The virtual file system behind IoFileMgrForUser. Devices:
//
//   - umd0: and disc0: route read-only to the mounted ISO9660
//     image (the host's umd.ISO).
//   - ms0: routes to the in-memory Memory Stick store the host
//     provides (read and write).
//   - host0: routes to the same store under host0: keys. This is
//     the psplink debug device: homebrew test frameworks (the
//     pspautotests corpus among them) write their results to
//     host0:/__testoutput.txt, so the store doubles as the test
//     result channel for the harness.
//   - fds 0, 1 and 2 are stdin (always EOF), stdout and stderr
//     (both append to the kernel console).
//
// Relative paths resolve against umd0:/ in this model. sceIo
// failures return the errno style kernel error codes.

const (
	fdConsoleIn  = 0
	fdConsoleOut = 1
	fdConsoleErr = 2
	firstUserFD  = 3
)

type fileDesc struct {
	isDir   bool
	writing bool

	// File backing (exactly one of isoPath / msKey is set).
	isoPath string
	msKey   string
	pos     int64

	// Directory backing.
	fromMS    bool
	keyPrefix string // store key prefix ("ms0:" or "host0:")
	dirNames  []string
	dirIdx    int
	dirPath   string
}

func splitDevice(path string) (device, rest string) {
	path = strings.ReplaceAll(path, "\\", "/")
	i := strings.IndexByte(path, ':')
	if i < 0 {
		return "umd0", path
	}
	return strings.ToLower(path[:i]), path[i+1:]
}

func cleanVPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	parts := []string{}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, seg)
		}
	}
	return "/" + strings.Join(parts, "/")
}

// isoStat resolves a path inside the mounted image to its entry.
func isoStat(iso *umd.ISO, path string) (umd.Entry, bool) {
	if path == "/" {
		return umd.Entry{Name: "/", Directory: true}, true
	}
	parent, name := splitParent(path)
	entries, err := iso.List(parent)
	if err != nil {
		return umd.Entry{}, false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return e, true
		}
	}
	return umd.Entry{}, false
}

func splitParent(path string) (dir, name string) {
	i := strings.LastIndexByte(path, '/')
	if i <= 0 {
		return "/", strings.TrimPrefix(path, "/")
	}
	return path[:i], path[i+1:]
}

// msFiles returns the host's memory stick store, creating the
// kernel's view when the host has none.
func (k *Kernel) msFiles() map[string][]byte {
	return k.host.MemoryStickFiles()
}

func (k *Kernel) openFile(path string, flags uint32) (*fileDesc, uint32) {
	device, rest := splitDevice(path)
	rest = cleanVPath(rest)
	write := flags&0x0003 != 0x0001 // PSP_O_RDONLY is 1; 2 and 3 write
	switch device {
	case "umd0", "disc0":
		iso := k.host.ISO()
		if iso == nil {
			return nil, ErrErrnoFileNotFound
		}
		info, ok := isoStat(iso, rest)
		if !ok {
			return nil, ErrErrnoFileNotFound
		}
		if info.Directory || write {
			return nil, ErrErrnoInvalidArgument // directories and writes are refused
		}
		return &fileDesc{isoPath: rest}, 0
	case "ms0", "host0":
		files := k.msFiles()
		if files == nil {
			return nil, ErrErrnoFileNotFound
		}
		key := device + ":" + rest
		if _, exists := files[key]; !exists {
			if flags&0x0200 == 0 { // PSP_O_CREAT
				return nil, ErrErrnoFileNotFound
			}
			files[key] = []byte{}
		}
		fd := &fileDesc{msKey: key, writing: true, fromMS: true}
		if flags&0x0400 != 0 { // PSP_O_TRUNC
			files[key] = files[key][:0]
		}
		if flags&0x0100 != 0 { // PSP_O_APPEND
			fd.pos = int64(len(files[key]))
		}
		return fd, 0
	}
	return nil, ErrErrnoFileNotFound
}

func (k *Kernel) openDir(path string) (*fileDesc, uint32) {
	device, rest := splitDevice(path)
	rest = cleanVPath(rest)
	switch device {
	case "umd0", "disc0":
		iso := k.host.ISO()
		if iso == nil {
			return nil, ErrErrnoFileNotFound
		}
		info, ok := isoStat(iso, rest)
		if !ok || !info.Directory {
			return nil, ErrErrnoFileNotFound
		}
		entries, err := iso.List(rest)
		if err != nil {
			return nil, ErrErrnoFileNotFound
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}
		return &fileDesc{isDir: true, dirNames: names, dirPath: rest}, 0
	case "ms0", "host0":
		prefix := device + ":" + rest
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		seen := map[string]bool{}
		names := []string{}
		for key := range k.msFiles() {
			if strings.HasPrefix(key, prefix) {
				name := strings.TrimPrefix(key, prefix)
				if i := strings.IndexByte(name, '/'); i >= 0 {
					name = name[:i]
				}
				if name != "" && !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
		sort.Strings(names)
		return &fileDesc{isDir: true, dirNames: names, dirPath: rest, fromMS: true, keyPrefix: device + ":"}, 0
	}
	return nil, ErrErrnoFileNotFound
}

func (k *Kernel) addFD(fd *fileDesc) int {
	id := k.nextFD
	k.nextFD++
	k.fds[id] = fd
	return id
}

// fileData returns the current bytes behind an open file.
func (k *Kernel) fileData(f *fileDesc) []byte {
	if f.msKey != "" {
		return k.msFiles()[f.msKey]
	}
	if f.isoPath != "" {
		if iso := k.host.ISO(); iso != nil {
			data, err := iso.ReadFile(f.isoPath)
			if err == nil {
				return data
			}
		}
	}
	return nil
}

// fillStat writes a SceIoStat (0x58 bytes) at ptr: mode and attr in
// the documented FIO forms, a 64 bit size, and zeroed timestamps.
func (k *Kernel) fillStat(ptr uint32, isDir bool, size uint32) {
	w := &guestWriter{k: k, addr: ptr}
	if isDir {
		w.u32(0x1000 | 0755) // st_mode: FIO_S_IFDIR | rwxr-xr-x
		w.u32(0x10)          // st_attr: FIO_SO_IFDIR
	} else {
		w.u32(0x2000 | 0644) // st_mode: FIO_S_IFREG | rw-r--r--
		w.u32(0x20)          // st_attr: FIO_SO_IFREG
	}
	w.u32(size)
	w.u32(0) // st_size high word
	for i := 0; i < 12; i++ {
		w.u32(0) // ctime, atime, mtime (16 byte ScePspDateTime each)
	}
	for i := 0; i < 6; i++ {
		w.u32(0) // st_private
	}
}

func hleIoOpen(k *Kernel, th *Thread) syscallAction {
	path := k.readCString(k.arg(th, 0), 1024)
	flags := k.arg(th, 1)
	fd, ecode := k.openFile(path, flags)
	if fd == nil {
		k.ret(th, ecode)
		return actNone
	}
	k.ret(th, uint32(k.addFD(fd)))
	return actNone
}

func hleIoClose(k *Kernel, th *Thread) syscallAction {
	fd := int(k.arg(th, 0))
	if fd >= 0 && fd < firstUserFD {
		k.ret(th, 0) // closing stdio is a no-op success
		return actNone
	}
	if _, ok := k.fds[fd]; !ok {
		k.ret(th, ErrErrnoBadFd)
		return actNone
	}
	delete(k.fds, fd)
	k.ret(th, 0)
	return actNone
}

func hleIoRead(k *Kernel, th *Thread) syscallAction {
	fd := int(k.arg(th, 0))
	buf := k.arg(th, 1)
	count := int(k.arg(th, 2))
	if fd == fdConsoleIn {
		k.ret(th, 0)
		return actNone
	}
	f, ok := k.fds[fd]
	if !ok || f.isDir {
		k.ret(th, ErrErrnoBadFd)
		return actNone
	}
	data := k.fileData(f)
	if f.pos >= int64(len(data)) {
		k.ret(th, 0)
		return actNone
	}
	n := count
	if int64(n) > int64(len(data))-f.pos {
		n = int(int64(len(data)) - f.pos)
	}
	k.writeBytes(buf, data[f.pos:f.pos+int64(n)])
	f.pos += int64(n)
	k.ret(th, uint32(n))
	return actNone
}

func hleIoWrite(k *Kernel, th *Thread) syscallAction {
	fd := int(k.arg(th, 0))
	buf := k.arg(th, 1)
	count := int(k.arg(th, 2))
	if fd == fdConsoleOut || fd == fdConsoleErr {
		k.writeConsole(k.readBytes(buf, count))
		k.ret(th, uint32(count))
		return actNone
	}
	f, ok := k.fds[fd]
	if !ok || f.isDir || f.msKey == "" {
		k.ret(th, ErrErrnoBadFd)
		return actNone
	}
	files := k.msFiles()
	data := files[f.msKey]
	end := f.pos + int64(count)
	if end > int64(len(data)) {
		grown := make([]byte, end)
		copy(grown, data)
		data = grown
	}
	copy(data[f.pos:end], k.readBytes(buf, count))
	files[f.msKey] = data
	f.pos = end
	k.ret(th, uint32(count))
	return actNone
}

func seekFD(k *Kernel, f *fileDesc, off int64, whence uint32) (int64, bool) {
	size := int64(len(k.fileData(f)))
	var np int64
	switch whence {
	case 0: // SEEK_SET
		np = off
	case 1: // SEEK_CUR
		np = f.pos + off
	case 2: // SEEK_END
		np = size + off
	default:
		return 0, false
	}
	if np < 0 {
		np = 0
	}
	f.pos = np
	return np, true
}

func hleIoLseek(k *Kernel, th *Thread) syscallAction {
	// o32 packing for (fd, SceOff, whence): the fd takes $a0, the
	// 64 bit offset the aligned $a2/$a3 pair, whence the first
	// stack slot at $sp+16.
	fd := int(k.arg(th, 0))
	off := int64(uint64(k.cpu.Reg(6)) | uint64(k.cpu.Reg(7))<<32)
	whence := k.mem.Read32(k.cpu.Reg(29) + 16)
	f, ok := k.fds[fd]
	if !ok || f.isDir {
		k.ret64(th, ^uint64(0))
		k.ret(th, ErrErrnoBadFd)
		return actNone
	}
	np, valid := seekFD(k, f, off, whence)
	if !valid {
		k.ret64(th, ^uint64(0))
		k.ret(th, ErrErrnoInvalidArgument)
		return actNone
	}
	k.ret64(th, uint64(np))
	return actNone
}

func hleIoLseek32(k *Kernel, th *Thread) syscallAction {
	fd := int(k.arg(th, 0))
	off := int64(int32(k.arg(th, 1)))
	whence := k.arg(th, 2)
	f, ok := k.fds[fd]
	if !ok || f.isDir {
		k.ret(th, ErrErrnoBadFd)
		return actNone
	}
	np, valid := seekFD(k, f, off, whence)
	if !valid {
		k.ret(th, ErrErrnoInvalidArgument)
		return actNone
	}
	k.ret(th, uint32(np))
	return actNone
}

func hleIoGetstat(k *Kernel, th *Thread) syscallAction {
	path := k.readCString(k.arg(th, 0), 1024)
	statPtr := k.arg(th, 1)
	device, rest := splitDevice(path)
	rest = cleanVPath(rest)
	switch device {
	case "umd0", "disc0":
		iso := k.host.ISO()
		if iso == nil {
			k.ret(th, ErrErrnoFileNotFound)
			return actNone
		}
		info, ok := isoStat(iso, rest)
		if !ok {
			k.ret(th, ErrErrnoFileNotFound)
			return actNone
		}
		k.fillStat(statPtr, info.Directory, info.Size)
		k.ret(th, 0)
	case "ms0", "host0":
		data, ok := k.msFiles()[device+":"+rest]
		if !ok {
			k.ret(th, ErrErrnoFileNotFound)
			return actNone
		}
		k.fillStat(statPtr, false, uint32(len(data)))
		k.ret(th, 0)
	default:
		k.ret(th, ErrErrnoFileNotFound)
	}
	return actNone
}

func hleIoDevctl(k *Kernel, th *Thread) syscallAction {
	// No device command is implemented in this model; the
	// documented invalid argument error is returned.
	k.ret(th, ErrErrnoInvalidArgument)
	return actNone
}

func hleIoDopen(k *Kernel, th *Thread) syscallAction {
	path := k.readCString(k.arg(th, 0), 1024)
	fd, ecode := k.openDir(path)
	if fd == nil {
		k.ret(th, ecode)
		return actNone
	}
	k.ret(th, uint32(k.addFD(fd)))
	return actNone
}

func hleIoDread(k *Kernel, th *Thread) syscallAction {
	fd := int(k.arg(th, 0))
	direntPtr := k.arg(th, 1)
	f, ok := k.fds[fd]
	if !ok || !f.isDir {
		k.ret(th, ErrErrnoBadFd)
		return actNone
	}
	if f.dirIdx >= len(f.dirNames) {
		k.ret(th, 0)
		return actNone
	}
	name := f.dirNames[f.dirIdx]
	f.dirIdx++
	// SceIoDirent: a SceIoStat (0x58 bytes) then d_name[256].
	child := f.dirPath + "/" + name
	if f.dirPath == "/" {
		child = "/" + name
	}
	isDir := false
	var size uint32
	if !f.fromMS {
		if iso := k.host.ISO(); iso != nil {
			if info, ok := isoStat(iso, child); ok {
				isDir = info.Directory
				size = info.Size
			}
		}
	} else if data, ok := k.msFiles()[f.keyPrefix+child]; ok {
		size = uint32(len(data))
	} else {
		isDir = true // an intermediate memory stick directory
	}
	k.fillStat(direntPtr, isDir, size)
	for i := 0; i < 256; i++ {
		b := byte(0)
		if i < len(name) {
			b = name[i]
		}
		k.mem.Write8(direntPtr+0x58+uint32(i), b)
	}
	k.ret(th, 1)
	return actNone
}

func hleIoDclose(k *Kernel, th *Thread) syscallAction {
	return hleIoClose(k, th)
}
