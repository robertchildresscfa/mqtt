package mqtt

import (
	"bytes"
	"errors"
	"io"
)

var (
	badMsgTypeError        = errors.New("mqtt: message type is invalid")
	badQosError            = errors.New("mqtt: QoS is invalid")
	badWillQosError        = errors.New("mqtt: will QoS is invalid")
	badLengthEncodingError = errors.New("mqtt: remaining length field exceeded maximum of 4 bytes")
	badReturnCodeError     = errors.New("mqtt: is invalid")
	dataExceedsPacketError = errors.New("mqtt: data exceeds packet length")
	msgTooLongError        = errors.New("mqtt: message is too long")
)

const (
	QosAtMostOnce = QosLevel(iota)
	QosAtLeastOnce
	QosExactlyOnce

	qosFirstInvalid
)

type QosLevel uint8

func (qos QosLevel) IsValid() bool {
	return qos < qosFirstInvalid
}

func (qos QosLevel) HasId() bool {
	return qos == QosAtLeastOnce || qos == QosExactlyOnce
}

type Header struct {
	MessageType     MessageType
	DupFlag, Retain bool
	QosLevel        QosLevel
}

type ConnectFlags struct {
	UsernameFlag, PasswordFlag, WillRetain, WillFlag, CleanSession bool
	WillQos                                                        QosLevel
}

type Mqtt struct {
	Header                    Header
	ProtocolName, TopicName   string
	ClientId                  string
	WillTopic, WillMessage    string
	Username, Password        string
	ProtocolVersion           uint8
	ConnectFlags              ConnectFlags
	KeepAliveTimer, MessageId uint16
	Data                      []byte
	Topics                    []string
	TopicsQos                 []uint8
	ReturnCode                ReturnCode
}

type MessageType uint8

func (mt MessageType) IsValid() bool {
	return mt >= MsgConnect && mt < msgTypeFirstInvalid
}

const (
	MsgConnect = MessageType(iota + 1)
	MsgConnAck
	MsgPublish
	MsgPubAck
	MsgPubRec
	MsgPubRel
	MsgPubComp
	MsgSubscribe
	MsgSubAck
	MsgUnsubscribe
	MsgUnsubAck
	MsgPingReq
	MsgPingResp
	MsgDisconnect

	msgTypeFirstInvalid
)

const (
	RetCodeAccepted = ReturnCode(iota)
	RetCodeUnacceptableProtocolVersion
	RetCodeIdentifierRejected
	RetCodeServerUnavailable
	RetCodeBadUsernameOrPassword
	RetCodeNotAuthorized

	retCodeFirstInvalid
)

type ReturnCode uint8

func (rc ReturnCode) IsValid() bool {
	return rc >= RetCodeAccepted && rc < retCodeFirstInvalid
}

func getUint8(r io.Reader, packetRemaining *int32) uint8 {
	if *packetRemaining < 1 {
		raiseError(dataExceedsPacketError)
	}

	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		raiseError(err)
	}
	*packetRemaining--

	return b[0]
}

func getUint16(r io.Reader, packetRemaining *int32) uint16 {
	if *packetRemaining < 2 {
		raiseError(dataExceedsPacketError)
	}

	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		raiseError(err)
	}
	*packetRemaining -= 2

	return uint16(b[0]<<8) + uint16(b[1])
}

func getString(r io.Reader, packetRemaining *int32) string {
	strLen := int(getUint16(r, packetRemaining))

	if int(*packetRemaining) < strLen {
		raiseError(dataExceedsPacketError)
	}

	b := make([]byte, strLen)
	if _, err := io.ReadFull(r, b); err != nil {
		raiseError(err)
	}
	*packetRemaining -= int32(strLen)

	return string(b)
}

func getHeader(r io.Reader) (Header, int32) {
	var buf [1]byte

	if _, err := io.ReadFull(r, buf[:]); err != nil {
		raiseError(err)
	}

	byte1 := buf[0]

	return Header{
		MessageType: MessageType(byte1 & 0xF0 >> 4),
		DupFlag:     byte1&0x08 > 0,
		QosLevel:    QosLevel(byte1 & 0x06 >> 1),
		Retain:      byte1&0x01 > 0,
	}, decodeLength(r)
}

func getConnectFlags(r io.Reader, packetRemaining *int32) ConnectFlags {
	bit := getUint8(r, packetRemaining)
	return ConnectFlags{
		UsernameFlag: bit&0x80 > 0,
		PasswordFlag: bit&0x40 > 0,
		WillRetain:   bit&0x20 > 0,
		WillQos:      QosLevel(bit & 0x18 >> 3),
		WillFlag:     bit&0x04 > 0,
		CleanSession: bit&0x02 > 0,
	}
}

func Decode(b []byte) (*Mqtt, error) {
	return DecodeRead(bytes.NewBuffer(b))
}

func DecodeRead(r io.Reader) (mqtt *Mqtt, err error) {
	defer func() {
		err = recoverError(err)
	}()

	mqtt = new(Mqtt)

	var packetRemaining int32
	mqtt.Header, packetRemaining = getHeader(r)

	if !mqtt.Header.MessageType.IsValid() {
		err = badMsgTypeError
		return
	}

	switch mqtt.Header.MessageType {
	case MsgConnect:
		{
			mqtt.ProtocolName = getString(r, &packetRemaining)
			mqtt.ProtocolVersion = getUint8(r, &packetRemaining)
			mqtt.ConnectFlags = getConnectFlags(r, &packetRemaining)
			mqtt.KeepAliveTimer = getUint16(r, &packetRemaining)
			mqtt.ClientId = getString(r, &packetRemaining)

			if mqtt.ConnectFlags.WillFlag {
				mqtt.WillTopic = getString(r, &packetRemaining)
				mqtt.WillMessage = getString(r, &packetRemaining)
			}
			if mqtt.ConnectFlags.UsernameFlag {
				mqtt.Username = getString(r, &packetRemaining)
			}
			if mqtt.ConnectFlags.PasswordFlag {
				mqtt.Password = getString(r, &packetRemaining)
			}
		}
	case MsgConnAck:
		{
			getUint8(r, &packetRemaining) // Skip reserved byte.
			mqtt.ReturnCode = ReturnCode(getUint8(r, &packetRemaining))
			if !mqtt.ReturnCode.IsValid() {
				return nil, badReturnCodeError
			}
		}
	case MsgPublish:
		{
			mqtt.TopicName = getString(r, &packetRemaining)
			if mqtt.Header.QosLevel.HasId() {
				mqtt.MessageId = getUint16(r, &packetRemaining)
			}
			mqtt.Data = make([]byte, packetRemaining)
			if _, err = io.ReadFull(r, mqtt.Data); err != nil {
				return nil, err
			}
		}
	case MsgPubAck, MsgPubRec, MsgPubRel, MsgPubComp, MsgUnsubAck:
		{
			mqtt.MessageId = getUint16(r, &packetRemaining)
		}
	case MsgSubscribe:
		{
			if mqtt.Header.QosLevel.HasId() {
				mqtt.MessageId = getUint16(r, &packetRemaining)
			}
			topics := make([]string, 0)
			topics_qos := make([]uint8, 0)
			for packetRemaining > 0 {
				topics = append(topics, getString(r, &packetRemaining))
				topics_qos = append(topics_qos, getUint8(r, &packetRemaining))
			}
			mqtt.Topics = topics
			mqtt.TopicsQos = topics_qos
		}
	case MsgSubAck:
		{
			mqtt.MessageId = getUint16(r, &packetRemaining)
			topics_qos := make([]uint8, 0)
			for packetRemaining > 0 {
				topics_qos = append(topics_qos, getUint8(r, &packetRemaining))
			}
			mqtt.TopicsQos = topics_qos
		}
	case MsgUnsubscribe:
		{
			if qos := mqtt.Header.QosLevel; qos == 1 || qos == 2 {
				mqtt.MessageId = getUint16(r, &packetRemaining)
			}
			topics := make([]string, 0)
			for packetRemaining > 0 {
				topics = append(topics, getString(r, &packetRemaining))
			}
			mqtt.Topics = topics
		}
	}
	return mqtt, nil
}

func setUint8(val uint8, buf *bytes.Buffer) {
	buf.WriteByte(byte(val))
}

func setUint16(val uint16, buf *bytes.Buffer) {
	buf.WriteByte(byte(val & 0xff00 >> 8))
	buf.WriteByte(byte(val & 0x00ff))
}

func setString(val string, buf *bytes.Buffer) {
	length := uint16(len(val))
	setUint16(length, buf)
	buf.WriteString(val)
}

func setHeader(header *Header, buf *bytes.Buffer) {
	val := byte(uint8(header.MessageType)) << 4
	val |= (boolToByte(header.DupFlag) << 3)
	val |= byte(header.QosLevel) << 1
	val |= boolToByte(header.Retain)
	buf.WriteByte(val)
}

func setConnectFlags(flags *ConnectFlags, buf *bytes.Buffer) {
	val := boolToByte(flags.UsernameFlag) << 7
	val |= boolToByte(flags.PasswordFlag) << 6
	val |= boolToByte(flags.WillRetain) << 5
	val |= byte(flags.WillQos) << 3
	val |= boolToByte(flags.WillFlag) << 2
	val |= boolToByte(flags.CleanSession) << 1
	buf.WriteByte(val)
}

func boolToByte(val bool) byte {
	if val {
		return byte(1)
	}
	return byte(0)
}

func Encode(mqtt *Mqtt) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := EncodeWrite(buf, mqtt)
	return buf.Bytes(), err
}

func EncodeWrite(w io.Writer, mqtt *Mqtt) (err error) {
	defer func() {
		err = recoverError(err)
	}()

	if err = valid(mqtt); err != nil {
		return
	}

	buf := new(bytes.Buffer)
	switch mqtt.Header.MessageType {
	case MsgConnect:
		{
			setString(mqtt.ProtocolName, buf)
			setUint8(mqtt.ProtocolVersion, buf)
			setConnectFlags(&mqtt.ConnectFlags, buf)
			setUint16(mqtt.KeepAliveTimer, buf)
			setString(mqtt.ClientId, buf)
			if mqtt.ConnectFlags.WillFlag {
				setString(mqtt.WillTopic, buf)
				setString(mqtt.WillMessage, buf)
			}
			if mqtt.ConnectFlags.UsernameFlag {
				setString(mqtt.Username, buf)
			}
			if mqtt.ConnectFlags.PasswordFlag {
				setString(mqtt.Password, buf)
			}
		}
	case MsgConnAck:
		{
			buf.WriteByte(byte(0))
			setUint8(uint8(mqtt.ReturnCode), buf)
		}
	case MsgPublish:
		{
			setString(mqtt.TopicName, buf)
			if mqtt.Header.QosLevel.HasId() {
				setUint16(mqtt.MessageId, buf)
			}
			buf.Write(mqtt.Data)
		}
	case MsgPubAck, MsgPubRec, MsgPubRel, MsgPubComp, MsgUnsubAck:
		{
			setUint16(mqtt.MessageId, buf)
		}
	case MsgSubscribe:
		{
			if mqtt.Header.QosLevel.HasId() {
				setUint16(mqtt.MessageId, buf)
			}
			for i := 0; i < len(mqtt.Topics); i += 1 {
				setString(mqtt.Topics[i], buf)
				setUint8(mqtt.TopicsQos[i], buf)
			}
		}
	case MsgSubAck:
		{
			setUint16(mqtt.MessageId, buf)
			for i := 0; i < len(mqtt.TopicsQos); i += 1 {
				setUint8(mqtt.TopicsQos[i], buf)
			}
		}
	case MsgUnsubscribe:
		{
			if mqtt.Header.QosLevel.HasId() {
				setUint16(mqtt.MessageId, buf)
			}
			for i := 0; i < len(mqtt.Topics); i += 1 {
				setString(mqtt.Topics[i], buf)
			}
		}
	}
	if buf.Len() > 268435455 {
		return msgTooLongError
	}

	headerBuf := new(bytes.Buffer)
	setHeader(&mqtt.Header, headerBuf)
	encodeLength(int32(buf.Len()), headerBuf)

	if _, err = w.Write(headerBuf.Bytes()); err != nil {
		return
	}
	if _, err = w.Write(buf.Bytes()); err != nil {
		return
	}

	return err
}

func valid(mqtt *Mqtt) error {
	if !mqtt.Header.MessageType.IsValid() {
		return badMsgTypeError
	}
	if !mqtt.Header.QosLevel.IsValid() {
		return badQosError
	}
	if !mqtt.ConnectFlags.WillQos.IsValid() {
		return badWillQosError
	}
	return nil
}

func decodeLength(r io.Reader) int32 {
	var v int32
	var buf [1]byte
	var shift uint
	for i := 0; i < 4; i++ {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			raiseError(err)
		}

		b := buf[0]
		v |= int32(b&0x7f) << shift

		if b&0x80 == 0 {
			return v
		}
		shift += 7
	}

	raiseError(badLengthEncodingError)
	panic("unreachable")
}

func encodeLength(length int32, buf *bytes.Buffer) {
	if length == 0 {
		buf.WriteByte(byte(0))
		return
	}
	var lbuf bytes.Buffer
	for length > 0 {
		digit := length % 128
		length = length / 128
		if length > 0 {
			digit = digit | 0x80
		}
		lbuf.WriteByte(byte(digit))
	}
	blen := lbuf.Bytes()
	for i := 1; i <= len(blen); i += 1 {
		buf.WriteByte(blen[len(blen)-i])
	}
}

// panicErr wraps an error that caused a problem that needs to bail out of the
// API, such that errors can be recovered and returned as errors from the
// public API.
type panicErr struct {
	err error
}

func (p panicErr) Error() string {
	return p.err.Error()
}

func raiseError(err error) {
	panic(panicErr{err})
}

// recoverError recovers any panic in flight and, iff it's an error from
// raiseError, will return the error. Otherwise re-raises the panic value.
// If no panic is in flight, it returns existingErr.
//
// This must be used in combination with a defer in all public API entry
// points where raiseError could be called.
func recoverError(existingErr error) error {
	if p := recover(); p != nil {
		if pErr, ok := p.(panicErr); ok {
			return pErr.err
		} else {
			panic(p)
		}
	}
	return existingErr
}
