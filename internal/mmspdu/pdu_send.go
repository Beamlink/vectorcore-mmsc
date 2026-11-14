package mmspdu

func NewSendReq(transactionID, from string, to []string) *PDU {
	headers := map[byte]HeaderValue{
		FieldFrom:          NewFromValue(from),
		FieldTransactionID: NewTextValue(FieldTransactionID, transactionID),
	}
	pdu := &PDU{
		MessageType:   MsgTypeSendReq,
		TransactionID: transactionID,
		MMSVersion:    "1.3",
		Headers:       headers,
	}
	for _, addr := range to {
		pdu.AddAddress(FieldTo, addr)
	}
	return pdu
}

func NewSendReqWithParts(transactionID, from string, to []string, parts []Part) *PDU {
	pdu := NewSendReq(transactionID, from, to)
	pdu.Headers[FieldContentType] = NewContentTypeValue("multipart/related", nil)
	pdu.Body = &MultipartBody{Parts: parts}
	return pdu
}

func NewSendConf(transactionID, messageID string, responseStatus byte) *PDU {
	return &PDU{
		MessageType:   MsgTypeSendConf,
		TransactionID: transactionID,
		MMSVersion:    "1.3",
		Headers: map[byte]HeaderValue{
			FieldTransactionID:  NewTextValue(FieldTransactionID, transactionID),
			FieldMessageID:      NewTextValue(FieldMessageID, messageID),
			FieldResponseStatus: NewTokenValue(FieldResponseStatus, responseStatus),
		},
	}
}
