package tppclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

// Chunked transfer (SPEC §4.3). No frame carries more than SingleFrameMaxBytes
// of ciphertext; a larger entry is uploaded in chunks and read back in ranges.
// The crypto is unchanged: the entry is sealed and opened whole, and only the
// transport splits it.

// SingleFrameMaxBytes is the most ciphertext one frame carries, in either
// direction. It is a protocol constant, the cap every relay has always had.
const SingleFrameMaxBytes = 10 << 20

// MaxCiphertextBytes is the per-entry cap of a relay that predates chunked
// transfer. A current relay states its own cap; see Limits.
//
// Deprecated: the cap is the relay's to state. Use Client.Limits.
const MaxCiphertextBytes = SingleFrameMaxBytes

// Entry requests carry their own deadline, one per request rather than one
// for the whole transfer: a 50 MB entry on a slow link takes as long as it
// takes, and a caller that bounded it as a whole would fail it for being
// large. What is bounded is each step — a request that moves n bytes gets
// CallTimeout plus the time n bytes take at MinTransferRate — so a transfer
// that has stalled still fails, and one that is merely slow does not.
const (
	// CallTimeout is the deadline of an entry request that moves no bulk.
	CallTimeout = 30 * time.Second

	// MinTransferRate is the slowest link, in bytes per second, an entry
	// request is given time for.
	MinTransferRate = 128 << 10
)

// entryCallTimeout is the deadline for one entry or store request, by what
// it moves; false for any other request, which keeps the caller's.
func entryCallTimeout(msg proto.Message) (time.Duration, bool) {
	var n int
	switch m := msg.(type) {
	case *tppv1.EntryPutRequest:
		n = len(m.GetCiphertext())
	case *tppv1.EntryUploadChunkRequest:
		n = len(m.GetData())
	case *tppv1.EntryFetchChunkRequest:
		n = int(min(m.GetLength(), SingleFrameMaxBytes)) //nolint:gosec // capped
	case *tppv1.EntryFetchRequest:
		// The response is up to one frame; its size is not known in advance.
		n = SingleFrameMaxBytes
	case *tppv1.StorePutRequest:
		n = len(m.GetCiphertext())
	case *tppv1.StoreFetchChunkRequest:
		n = int(min(m.GetLength(), SingleFrameMaxBytes)) //nolint:gosec // capped
	case *tppv1.EntryLimitsRequest, *tppv1.EntryLatestRequest, *tppv1.EntryHistoryRequest,
		*tppv1.EntryUploadBeginRequest, *tppv1.EntryUploadAbortRequest,
		*tppv1.StoreLimitsRequest, *tppv1.StoreLinksRequest, *tppv1.StoreListRequest,
		*tppv1.StoreUploadBeginRequest, *tppv1.StoreUpdateMetaRequest, *tppv1.StoreDeleteRequest:
	default:
		return 0, false
	}
	return CallTimeout + time.Duration(n)*time.Second/MinTransferRate, true
}

// errUnsupported matches a relay's answer to a message type it does not know.
var errUnsupported error = codeError(tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE)

// Limits are a relay's entry limits, in ciphertext bytes.
type Limits struct {
	// MaxEntryBytes is the largest entry the relay accepts.
	MaxEntryBytes int64

	// SingleFrameMaxBytes is the largest ciphertext one frame carries.
	SingleFrameMaxBytes int64

	// ChunkBytes is the chunk size; zero when the relay cannot chunk.
	ChunkBytes int64

	// InlineMaxBytes is the inline threshold; zero when not reported.
	InlineMaxBytes int64

	// HeaderV2 reports that the relay accepts version 2 entry headers
	// (/spec/crypto.md §5.5.1). A relay that predates them refuses them, so
	// this client writes version 1 to it.
	HeaderV2 bool
}

// Chunked reports whether the relay accepts entries larger than one frame.
func (l Limits) Chunked() bool { return l.ChunkBytes > 0 }

// legacyLimits are what a relay that predates chunked transfer allows.
var legacyLimits = Limits{MaxEntryBytes: SingleFrameMaxBytes, SingleFrameMaxBytes: SingleFrameMaxBytes}

// Direction says which way a transfer runs.
type Direction int

const (
	// Upload is this client writing an entry.
	Upload Direction = iota + 1
	// Download is this client reading one.
	Download
)

// Progress reports a chunked transfer. Done and Total are ciphertext bytes.
// It is reported only for entries larger than one frame; anything smaller is
// a single round trip.
type Progress struct {
	EntryID   string
	Direction Direction
	Done      int64
	Total     int64
}

// Limits returns the relay's entry limits, asking once per connection. A relay
// that predates chunked transfer answers that it does not know the question,
// and gets the limits it has always had.
func (c *Client) Limits(ctx context.Context) (Limits, error) {
	conn, err := c.connection(ctx)
	if err != nil {
		return Limits{}, err
	}
	c.mu.Lock()
	if c.limitsConn == conn {
		l := c.limits
		c.mu.Unlock()
		return l, nil
	}
	c.mu.Unlock()

	var resp tppv1.EntryLimitsResponse
	l := legacyLimits
	callCtx, cancel := context.WithTimeout(ctx, CallTimeout)
	err = conn.call(callCtx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_LIMITS_REQUEST, &tppv1.EntryLimitsRequest{}, &resp)
	cancel()
	switch {
	case errors.Is(err, errUnsupported):
	case err != nil:
		return Limits{}, err
	default:
		l = Limits{
			MaxEntryBytes:       int64(resp.GetMaxEntryBytes()),       //nolint:gosec // a relay's own limits
			SingleFrameMaxBytes: int64(resp.GetSingleFrameMaxBytes()), //nolint:gosec // a relay's own limits
			ChunkBytes:          int64(resp.GetChunkBytes()),          //nolint:gosec // a relay's own limits
			InlineMaxBytes:      int64(resp.GetInlineMaxBytes()),      //nolint:gosec // a relay's own limits
			HeaderV2:            resp.GetHeaderV2(),
		}
		// Never trust a relay to raise the frame cap: the read limit on
		// this side is fixed.
		l.SingleFrameMaxBytes = min(max(l.SingleFrameMaxBytes, 1), SingleFrameMaxBytes)
		l.ChunkBytes = min(l.ChunkBytes, SingleFrameMaxBytes)
	}

	c.mu.Lock()
	c.limits, c.limitsConn = l, conn
	c.mu.Unlock()
	return l, nil
}

// putChunked uploads a sealed container larger than one frame. On any failure
// the upload is aborted, so the room the relay reserved for it is given back
// at once rather than at its timeout.
func (c *Client) putChunked(ctx context.Context, entryID string, epoch uint64, container, header []byte, l Limits) (*tppv1.EntryPutResponse, error) {
	size := int64(len(container))
	var begin tppv1.EntryUploadBeginResponse
	err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST, &tppv1.EntryUploadBeginRequest{
		EntryId: entryID,
		Epoch:   epoch,
		Size:    uint64(size), //nolint:gosec // a length
	}, &begin)
	if err != nil {
		return nil, err
	}
	handle := begin.GetUploadHandle()
	chunk := int64(begin.GetChunkBytes()) //nolint:gosec // the relay's own chunk size
	if chunk <= 0 || chunk > SingleFrameMaxBytes {
		chunk = l.ChunkBytes
	}

	abort := func(cause error) error {
		_ = c.call(context.WithoutCancel(ctx), tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST,
			&tppv1.EntryUploadAbortRequest{UploadHandle: handle}, &tppv1.EntryUploadAbortResponse{})
		return cause
	}

	c.progress(Progress{EntryID: entryID, Direction: Upload, Total: size})
	for i, off := uint32(0), int64(0); off < size; i, off = i+1, off+chunk {
		end := min(off+chunk, size)
		var ack tppv1.EntryUploadChunkResponse
		err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST, &tppv1.EntryUploadChunkRequest{
			UploadHandle: handle,
			Index:        i,
			Data:         container[off:end],
		}, &ack)
		if err != nil {
			return nil, abort(err)
		}
		c.progress(Progress{EntryID: entryID, Direction: Upload, Done: end, Total: size})
	}

	var resp tppv1.EntryPutResponse
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		EntryId: entryID,
		Epoch:   epoch,
		Size:    uint64(size), //nolint:gosec // a length
		Header:  header,
		Body:    &tppv1.EntryPutRequest_UploadHandle{UploadHandle: handle},
	}, &resp)
	if err != nil {
		// The relay ends the upload itself on an epoch conflict; for any
		// other failure the abort is what releases the room.
		return nil, abort(err)
	}
	return &resp, nil
}

// fetchRanges reads an entry back one range at a time. The first range also
// brings the metadata, which says how many follow.
func (c *Client) fetchRanges(ctx context.Context, entryID string, l Limits) (EntryMeta, []byte, error) {
	var (
		meta EntryMeta
		buf  []byte
	)
	for {
		var resp tppv1.EntryFetchChunkResponse
		err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_CHUNK_REQUEST, &tppv1.EntryFetchChunkRequest{
			EntryId: entryID,
			Offset:  uint64(len(buf)),
			Length:  uint64(l.ChunkBytes), //nolint:gosec // the relay's own chunk size
		}, &resp)
		if err != nil {
			return EntryMeta{}, nil, err
		}
		m := c.entryMeta(resp.GetMeta())
		switch {
		case buf == nil:
			if m.Size <= 0 || m.Size > max(l.MaxEntryBytes, SingleFrameMaxBytes) {
				return EntryMeta{}, nil, fmt.Errorf("tppclient: entry %s reports %d bytes, outside this relay's limits", entryID, m.Size)
			}
			meta = m
			buf = make([]byte, 0, m.Size)
		case m.Size != meta.Size || m.ID != meta.ID:
			return EntryMeta{}, nil, fmt.Errorf("tppclient: entry %s changed while it was being read", entryID)
		}
		data := resp.GetData()
		if resp.GetOffset() != uint64(len(buf)) || len(data) == 0 || int64(len(buf)+len(data)) > meta.Size {
			return EntryMeta{}, nil, fmt.Errorf("tppclient: entry %s: the relay returned a range that does not continue the entry", entryID)
		}
		buf = append(buf, data...)
		if meta.Size > SingleFrameMaxBytes {
			c.progress(Progress{EntryID: entryID, Direction: Download, Done: int64(len(buf)), Total: meta.Size})
		}
		if int64(len(buf)) == meta.Size {
			return meta, buf, nil
		}
	}
}

func (c *Client) progress(p Progress) {
	if c.handlers.OnProgress != nil {
		c.handlers.OnProgress(p)
	}
}
