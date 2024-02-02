package znet

const (
	block1k = 1 * 1024
	block2k = 2 * 1024
	block4k = 4 * 1024
	block8k = 8 * 1024

	pageSize = block8k
)

type Reader interface {
	Next(n int) (p []byte, err error)
	Peek(n int) (buf []byte, err error)
	Skip(n int) (err error)
	ReadSlice(delim byte) (line []byte, err error)
	ReadString(n int) (s string, err error)
	ReadBytes(n int) (p []byte, err error)
	ReadByte() (b byte, err error)
	Slice(n int) (r Reader, err error)
	Release() (err error)
	Len() (length int)
}

type Writer interface {
	Malloc(n int) (buf []byte, err error)
	WriteString(s string) (n int, err error)
	WriteBytes(b []byte) (n int, err error)
	WriteByte(b byte) (err error)
	WriteDirect(p []byte, remainCap int) error
	MallocAck(n int) (err error)
	Append(w Writer) (err error)
	Flush() (err error)
	MallocLen() (length int)
}
