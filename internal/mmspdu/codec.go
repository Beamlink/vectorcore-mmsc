package mmspdu

import (
	"fmt"
	"sort"
)

type PDU struct {
	MessageType   uint8
	TransactionID string
	MMSVersion    string
	Headers       map[byte]HeaderValue
	HeaderOrder   []byte
	// Extra holds repeated To/Cc/Bcc values after the first one kept in Headers.
	Extra []HeaderValue
	Body  *MultipartBody
}

// repeatableFields may appear more than once in a PDU (one address each).
var repeatableFields = map[byte]bool{FieldTo: true, FieldCC: true, FieldBCC: true}

// AddressValues returns every value of field: the Headers entry, then any repeats.
func (p *PDU) AddressValues(field byte) []HeaderValue {
	if p == nil {
		return nil
	}
	var out []HeaderValue
	if value, ok := p.Headers[field]; ok {
		out = append(out, value)
	}
	for _, value := range p.Extra {
		if value.Field == field {
			out = append(out, value)
		}
	}
	return out
}

// AddAddress appends one address value for a repeatable field.
func (p *PDU) AddAddress(field byte, address string) {
	value := NewAddressValue(field, address)
	if p.Headers == nil {
		p.Headers = make(map[byte]HeaderValue)
	}
	if _, ok := p.Headers[field]; !ok {
		p.Headers[field] = value
		return
	}
	p.Extra = append(p.Extra, value)
}

type MultipartBody struct {
	Parts []Part
}

type Part struct {
	Headers           map[string]string
	ContentType       string
	ContentTypeParams map[string]string
	Data              []byte
}

func Decode(data []byte) (*PDU, error) {
	headers, headerOrder, extra, body, err := decodePDUSections(data)
	if err != nil {
		return nil, err
	}

	pdu := &PDU{Headers: headers, HeaderOrder: headerOrder, Extra: extra, Body: body}
	if value, ok := headers[FieldMessageType]; ok {
		token, err := value.Token()
		if err != nil {
			return nil, fmt.Errorf("decode message type: %w", err)
		}
		pdu.MessageType = token
	}
	if value, ok := headers[FieldTransactionID]; ok {
		text, err := value.Text()
		if err != nil {
			return nil, fmt.Errorf("decode transaction id: %w", err)
		}
		pdu.TransactionID = text
	}
	if value, ok := headers[FieldMMSVersion]; ok {
		version, err := value.Integer()
		if err != nil {
			return nil, fmt.Errorf("decode mms version: %w", err)
		}
		pdu.MMSVersion = decodeVersionByte(byte(version))
	}
	return pdu, nil
}

func Encode(pdu *PDU) ([]byte, error) {
	if pdu == nil {
		return nil, fmt.Errorf("mmspdu: nil pdu")
	}
	headers := make(map[byte]HeaderValue, len(pdu.Headers)+3)
	for k, v := range pdu.Headers {
		headers[k] = v
	}
	if pdu.MessageType != 0 {
		headers[FieldMessageType] = NewTokenValue(FieldMessageType, pdu.MessageType)
	}
	if pdu.TransactionID != "" {
		headers[FieldTransactionID] = NewTextValue(FieldTransactionID, pdu.TransactionID)
	}
	if pdu.MMSVersion != "" {
		headers[FieldMMSVersion] = NewShortIntegerValue(FieldMMSVersion, encodeVersionString(pdu.MMSVersion))
	}

	if pdu.Body == nil {
		return encodeOrderedHeaders(headers, pdu.HeaderOrder, pdu.Extra)
	}

	if _, ok := headers[FieldContentType]; !ok {
		headers[FieldContentType] = NewContentTypeValue("multipart/related", nil)
	}
	return encodePDUWithBody(headers, pdu.HeaderOrder, pdu.Extra, pdu.Body)
}

func encodeVersionString(value string) byte {
	switch value {
	case "1.0":
		return 0x10
	case "1.1":
		return 0x11
	case "1.2":
		return 0x12
	case "1.3":
		return 0x13
	default:
		return 0x13
	}
}

func decodeVersionByte(value byte) string {
	switch value {
	case 0x10:
		return "1.0"
	case 0x11:
		return "1.1"
	case 0x12:
		return "1.2"
	case 0x13:
		return "1.3"
	default:
		return ""
	}
}

func encodePDUWithBody(headers map[byte]HeaderValue, headerOrder []byte, extra []HeaderValue, body *MultipartBody) ([]byte, error) {
	bodyData, err := EncodeMultipart(body)
	if err != nil {
		return nil, err
	}

	ctValue, ok := headers[FieldContentType]
	if !ok {
		return nil, fmt.Errorf("mmspdu: body present without content type")
	}

	headerCopy := make(map[byte]HeaderValue, len(headers)-1)
	for field, value := range headers {
		if field == FieldContentType {
			continue
		}
		headerCopy[field] = value
	}

	prefix, err := encodeOrderedHeaders(headerCopy, headerOrder, extra)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), prefix...)
	out = append(out, encodeHeaderName(FieldContentType)...)
	out = append(out, ctValue.Raw...)
	out = append(out, bodyData...)
	return out, nil
}

func encodeOrderedHeaders(headers map[byte]HeaderValue, originalOrder []byte, extra []HeaderValue) ([]byte, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if len(originalOrder) != 0 {
		return encodeHeadersBySequence(headers, originalOrder, extra)
	}
	order := canonicalHeaderOrder(headers)
	return encodeHeadersBySequence(headers, order, extra)
}

func canonicalHeaderOrder(headers map[byte]HeaderValue) []byte {
	order := make([]byte, 0, len(headers))
	seen := make(map[byte]struct{}, len(headers))
	for _, field := range []byte{FieldMessageType, FieldTransactionID, FieldMMSVersion} {
		if _, ok := headers[field]; ok {
			order = append(order, field)
			seen[field] = struct{}{}
		}
	}
	for field := range headers {
		if _, ok := seen[field]; ok {
			continue
		}
		order = append(order, field)
	}
	if len(order) > len(seen) {
		sort.Slice(order[len(seen):], func(i, j int) bool {
			return order[len(seen)+i] < order[len(seen)+j]
		})
	}
	return order
}

func encodeHeadersBySequence(headers map[byte]HeaderValue, order []byte, extra []HeaderValue) ([]byte, error) {
	var out []byte
	emit := func(field byte, value HeaderValue) error {
		if len(value.Raw) == 0 {
			return fmt.Errorf("mmspdu: header %x has empty value", field)
		}
		out = append(out, encodeHeaderName(field)...)
		out = append(out, value.Raw...)
		return nil
	}
	pending := make(map[byte][]HeaderValue)
	for _, value := range extra {
		if _, ok := headers[value.Field]; ok {
			pending[value.Field] = append(pending[value.Field], value)
		}
	}
	// emitRepeats writes a field's remaining repeats right after its first value.
	emitRepeats := func(field byte) error {
		for _, value := range pending[field] {
			if err := emit(field, value); err != nil {
				return err
			}
		}
		delete(pending, field)
		return nil
	}

	seen := make(map[byte]struct{}, len(headers))
	for _, field := range order {
		if _, ok := seen[field]; ok {
			continue // repeats are emitted with the first occurrence
		}
		value, ok := headers[field]
		if !ok {
			continue
		}
		if err := emit(field, value); err != nil {
			return nil, err
		}
		if err := emitRepeats(field); err != nil {
			return nil, err
		}
		seen[field] = struct{}{}
	}
	if len(seen) == len(headers) {
		return out, nil
	}

	remaining := make([]int, 0, len(headers)-len(seen))
	for field := range headers {
		if _, ok := seen[field]; ok {
			continue
		}
		remaining = append(remaining, int(field))
	}
	sort.Ints(remaining)
	for _, key := range remaining {
		field := byte(key)
		if err := emit(field, headers[field]); err != nil {
			return nil, err
		}
		if err := emitRepeats(field); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func decodePDUSections(data []byte) (map[byte]HeaderValue, []byte, []HeaderValue, *MultipartBody, error) {
	reader := newByteReader(data)
	headers := make(map[byte]HeaderValue)
	order := make([]byte, 0, 8)
	var extra []HeaderValue

	for reader.Remaining() > 0 {
		field, err := decodeHeaderName(reader)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		kind := kindForField(field)
		raw, err := decodeHeaderValue(reader, field, kind)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		value := HeaderValue{Field: field, Kind: kind, Raw: raw}
		if _, dup := headers[field]; dup && repeatableFields[field] {
			extra = append(extra, value)
		} else {
			headers[field] = value
		}
		order = append(order, field)

		if field == FieldContentType && reader.Remaining() > 0 {
			body, err := DecodeMultipart(reader.data[reader.pos:])
			if err == nil {
				return headers, order, extra, body, nil
			}
			return nil, nil, nil, nil, fmt.Errorf("decode multipart body: %w", err)
		}
	}
	return headers, order, extra, nil, nil
}
