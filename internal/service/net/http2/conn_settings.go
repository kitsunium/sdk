package http2

import (
	"encoding/binary"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// settingsFrame handles SETTINGS, §6.5: an ACK moves the decoder's bound, a
// new set is applied and acknowledged in the same batch.
func (f *feedRun) settingsFrame() int {
	//: SETTINGS belongs to the connection, §6.5.
	if f.h.StreamID != 0 {
		f.connError(corehttp2.ErrCodeProtocol)
		return 0
	}
	//: an ACK carries no payload; it acknowledges the bounds we advertised.
	if f.h.Flags&corehttp2.FlagAck != 0 {
		f.settingsAck()
		return f.frameLen()
	}
	//: a SETTINGS frame's length is a multiple of six, §6.5.
	if f.h.Length%settingLenLocal != 0 {
		f.connError(corehttp2.ErrCodeFrameSize)
		return 0
	}
	f.c.set(flagSettings)
	f.spendControl()
	f.applySettings()
	//: the ACK leaves before anything the new settings released.
	if !f.c.has(flagClosing) {
		f.dst = AppendSettingsAck(f.dst)
		f.l.flushHolds(f)
	}
	return f.frameLen()
}

// settingsAck handles a SETTINGS ACK: the peer has our table bound.
func (f *feedRun) settingsAck() {
	//: an ACK with a payload is malformed, §6.5.
	if f.h.Length != settingsAckLen {
		f.connError(corehttp2.ErrCodeFrameSize)
		return
	}
	f.spendControl()
	f.c.decoder.acknowledge()
}

// applySettings walks the frame's parameters in order, §6.5.3.
func (f *feedRun) applySettings() {
	payload := f.payload
	//: every six-byte parameter; a value the protocol refuses ends the connection.
	for len(payload) > 0 && !f.c.has(flagClosing) {
		id := corehttp2.SettingID(binary.BigEndian.Uint16(payload))
		val := binary.BigEndian.Uint32(payload[2:])
		payload = payload[settingLenLocal:]
		f.applySetting(id, val)
	}
}

// applySetting applies one parameter, §6.5.2.
func (f *feedRun) applySetting(id corehttp2.SettingID, val uint32) {
	//: the parameters the server honours, and the two the protocol constrains.
	switch id {
	case corehttp2.SettingInitialWindowSize:
		f.applyInitialWindow(val)
	case corehttp2.SettingMaxFrameSize:
		f.applyMaxFrame(val)
	case corehttp2.SettingEnablePush:
		f.applyEnablePush(val)
	default:
		//: table size, stream count and list size bound what a server would push or encode: this one encodes without a table.
	}
}

// applyEnablePush refuses a value the setting cannot take, §6.5.2.
func (f *feedRun) applyEnablePush(val uint32) {
	//: 0 or 1, nothing else.
	if val > 1 {
		f.connError(corehttp2.ErrCodeProtocol)
	}
}

// applyMaxFrame moves the frame size the server writes to, §6.5.2.
func (f *feedRun) applyMaxFrame(val uint32) {
	//: the protocol's range.
	if int64(val) < int64(corehttp2.DefaultMaxFrame) || int64(val) > int64(corehttp2.MaxFrameLimit) {
		f.connError(corehttp2.ErrCodeProtocol)
		return
	}
	f.c.peerMaxFrame = int32(val)
}

// applyInitialWindow adjusts every open stream's send window by the change in
// the initial window, §6.9.2: it may push a window negative, and a window past
// the protocol's maximum ends the connection.
func (f *feedRun) applyInitialWindow(val uint32) {
	//: the protocol's maximum, §6.5.2.
	if int64(val) > int64(corehttp2.MaxWindow) {
		f.connError(corehttp2.ErrCodeFlowControl)
		return
	}
	delta := int64(val) - int64(f.c.initialSend)
	//: every open stream shifts by the same delta, §6.9.2.
	for idx := f.c.head; idx != noIndex; idx = f.l.streams[idx].next {
		st := &f.l.streams[idx]
		next := int64(st.sendWindow) + delta
		//: past the protocol's window maximum, §6.9.2.
		if next > int64(corehttp2.MaxWindow) {
			f.connError(corehttp2.ErrCodeFlowControl)
			return
		}
		st.sendWindow = int32(next)
	}
	f.c.initialSend = int32(val)
}
