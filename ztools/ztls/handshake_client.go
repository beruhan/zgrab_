// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ztls

import (
	"bytes"
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"

	"github.com/zmap/zgrab/ztools/x509"
)

type clientHandshakeState struct {
	c               *Conn
	serverHello     *serverHelloMsg
	hello           *clientHelloMsg
	suite           *cipherSuite
	finishedHash    finishedHash
	masterSecret    []byte
	preMasterSecret []byte
	session         *ClientSessionState
}

type SniExtension struct {
	Domains []string
}

func (e SniExtension) Marshal() []byte {
	result := []byte{}
	for _, domain := range e.Domains {
		current := make([]byte, 2+len(domain))
		copy(current[2:], []byte(domain))
		current[0] = uint8(len(domain) >> 8)
		current[1] = uint8(len(domain))
		result = append(result, current...)
	}
	sniHeader := make([]byte, 3)
	sniHeader[0] = uint8((len(result) + 1) >> 8)
	sniHeader[1] = uint8((len(result) + 1))
	sniHeader[2] = 0
	result = append(sniHeader, result...)

	extHeader := make([]byte, 4)
	extHeader[0] = 0
	extHeader[1] = 0
	extHeader[2] = uint8((len(result)) >> 8)
	extHeader[3] = uint8((len(result)))
	result = append(extHeader, result...)

	return result
}

type ALPNExtension struct {
	Protocols []string
}

func (e ALPNExtension) Marshal() []byte {
	result := []byte{}
	for _, protocol := range e.Protocols {
		current := make([]byte, 2+len(protocol))
		copy(current[2:], []byte(protocol))
		current[0] = uint8(len(protocol) >> 8)
		current[1] = uint8(len(protocol))
		result = append(result, current...)
	}
	alpnHeader := make([]byte, 2)
	alpnHeader[0] = uint8((len(result)) >> 8)
	alpnHeader[1] = uint8((len(result)))
	result = append(alpnHeader, result...)

	extHeader := make([]byte, 4)
	extHeader[0] = 0
	extHeader[1] = 0
	extHeader[2] = uint8((len(result)) >> 8)
	extHeader[3] = uint8((len(result)))
	result = append(extHeader, result...)

	return result
}

type SecureRenegotiationExtension struct {
}

func (e SecureRenegotiationExtension) Marshal() []byte {
	result := make([]byte, 5)
	result[0] = byte(extensionRenegotiationInfo >> 8)
	result[1] = byte(extensionRenegotiationInfo & 0xff)
	result[2] = 0
	result[3] = 1
	result[4] = 0
	return result
}

type ExtendedMasterSecretExtension struct {
}

func (e ExtendedMasterSecretExtension) Marshal() []byte {
	result := make([]byte, 4)
	result[0] = byte(extensionExtendedMasterSecret >> 8)
	result[1] = byte(extensionExtendedMasterSecret & 0xff)
	result[2] = 0
	result[3] = 0
	return result
}

type NextProtocolNegotiationExtension struct {
}

func (e NextProtocolNegotiationExtension) Marshal() []byte {
	result := make([]byte, 4)
	result[0] = byte(extensionNextProtoNeg >> 8)
	result[1] = byte(extensionNextProtoNeg & 0xff)
	result[2] = 0
	result[3] = 0
	return result
}

type StatusRequestExtension struct {
}

func (e StatusRequestExtension) Marshal() []byte {
	result := make([]byte, 4)
	result[0] = byte(extensionStatusRequest >> 8)
	result[1] = byte(extensionStatusRequest & 0xff)
	result[2] = 0
	result[3] = 0
	return result
}

type SCTExtension struct {
}

func (e SCTExtension) Marshal() []byte {
	result := make([]byte, 4)
	result[0] = byte(extensionSCT >> 8)
	result[1] = byte(extensionSCT & 0xff)
	result[2] = 0
	result[3] = 0
	return result
}

type SupportedCurvesExtension struct {
	Curves []CurveID
}

func (e SupportedCurvesExtension) Marshal() []byte {
	result := make([]byte, 6+2*len(e.Curves))
	result[0] = byte(extensionSupportedCurves >> 8)
	result[1] = byte(extensionSupportedCurves & 0xff)
	result[2] = uint8((2 + 2*len(e.Curves)) >> 8)
	result[3] = uint8((2 + 2*len(e.Curves)))
	result[4] = uint8((2 * len(e.Curves)) >> 8)
	result[5] = uint8((2 * len(e.Curves)))
	for i, curve := range e.Curves {
		result[6+2*i] = uint8(curve >> 8)
		result[7+2*i] = uint8(curve)
	}
	return result
}

type PointFormatExtension struct {
	Formats []uint8
}

func (e PointFormatExtension) Marshal() []byte {
	result := make([]byte, 5+len(e.Formats))
	result[0] = byte(extensionSupportedPoints >> 8)
	result[1] = byte(extensionSupportedPoints & 0xff)
	result[2] = uint8((1 + len(e.Formats)) >> 8)
	result[3] = uint8((1 + len(e.Formats)))
	result[4] = uint8((len(e.Formats)))
	for i, format := range e.Formats {
		result[5+i] = format
	}
	return result
}

type SessionTicketExtension struct {
	Ticket []byte
}

func (e SessionTicketExtension) Marshal() []byte {
	result := make([]byte, 4+len(e.Ticket))
	result[0] = byte(extensionSessionTicket >> 8)
	result[1] = byte(extensionSessionTicket & 0xff)
	result[2] = uint8(len(e.Ticket) >> 8)
	result[3] = uint8(len(e.Ticket))
	if len(e.Ticket) > 0 {
		copy(result[4:], e.Ticket)
	}
	return result
}

type SignatureAlgorithmExtension struct {
	SignatureAndHashes []signatureAndHash
}

func (e SignatureAlgorithmExtension) Marshal() []byte {
	result := make([]byte, 6+2*len(e.SignatureAndHashes))
	result[0] = byte(extensionSignatureAlgorithms >> 8)
	result[1] = byte(extensionSignatureAlgorithms & 0xff)
	result[2] = uint8((2 + 2*len(e.SignatureAndHashes)) >> 8)
	result[3] = uint8((2 + 2*len(e.SignatureAndHashes)))
	result[4] = uint8((2 * len(e.SignatureAndHashes)) >> 8)
	result[5] = uint8((2 * len(e.SignatureAndHashes)))
	for i, pair := range e.SignatureAndHashes {
		result[6+2*i] = uint8(pair.hash)
		result[7+2*i] = uint8(pair.signature)
	}
	return result
}

type ClientHelloConfiguration struct {
	HandshakeVersion   uint16
	ClientRandom       []byte
	SessionID          []byte
	CipherSuites       []uint16
	CompressionMethods []uint8
	Extensions         []ClientExtension
}

func (c *ClientHelloConfiguration) ValidateExtensions() error {
	for _, ext := range c.Extensions {
		switch ext.(type) {
		case PointFormatExtension:
			for _, format := range ext.(PointFormatExtension).Formats {
				if format != pointFormatUncompressed {
					return errors.New(fmt.Sprintf("Unsupported EC Point Format %d", format))
				}
			}
		case SignatureAlgorithmExtension:
			for _, algs := range ext.(SignatureAlgorithmExtension).SignatureAndHashes {
				found := false
				for _, supported := range supportedSKXSignatureAlgorithms {
					if algs.hash == supported.hash && algs.signature == supported.signature {
						found = true
						break
					}
				}
				if !found {
					return errors.New(fmt.Sprintf("Unsupported Hash and Signature Algorithm (%d, %d)", algs.hash, algs.signature))
				}
			}
		}
	}
	return nil
}

func (c *ClientHelloConfiguration) marshal(config *Config) ([]byte, error) {
	if err := c.ValidateExtensions(); err != nil {
		return nil, err
	}
	head := make([]byte, 38)
	head[0] = 1
	head[4] = uint8(c.HandshakeVersion >> 8)
	head[5] = uint8(c.HandshakeVersion)
	if len(c.ClientRandom) == 32 {
		copy(head[6:38], c.ClientRandom[0:32])
	} else {
		_, err := io.ReadFull(config.rand(), head[6:38])
		if err != nil {
			return nil, errors.New("tls: short read from Rand: " + err.Error())
		}
	}

	sessionID := make([]byte, len(c.SessionID)+1)
	sessionID[0] = uint8(len(c.SessionID))
	if len(c.SessionID) > 0 {
		copy(sessionID[1:], c.SessionID)
	}

	ciphers := make([]byte, 2+2*len(c.CipherSuites))
	ciphers[0] = uint8(len(c.CipherSuites) >> 7)
	ciphers[1] = uint8(len(c.CipherSuites) << 1)
	for i, suite := range c.CipherSuites {
		if !config.ForceSuites {
			found := false
			for _, impl := range implementedCipherSuites {
				if impl.id == suite {
					found = true
				}
			}
			if !found {
				return nil, errors.New(fmt.Sprintf("tls: unimplemented cipher suite %d", suite))
			}
		}

		ciphers[2+i*2] = uint8(suite >> 8)
		ciphers[3+i*2] = uint8(suite)
	}

	compressions := make([]byte, len(c.CompressionMethods)+1)
	compressions[0] = uint8(len(c.CompressionMethods))
	if len(c.CompressionMethods) > 0 {
		copy(compressions[1:], c.CompressionMethods)
		if c.CompressionMethods[0] != 0 {
			return nil, errors.New(fmt.Sprintf("tls: unimplemented compression method %d", c.CompressionMethods[0]))
		} else if len(c.CompressionMethods) > 1 {
			return nil, errors.New(fmt.Sprintf("tls: unimplemented compression method %d", c.CompressionMethods[1]))
		}
	} else {
		return nil, errors.New("tls: no compression method")
	}

	var extensions []byte
	for _, ext := range c.Extensions {
		extensions = append(extensions, ext.Marshal()...)
	}
	if len(extensions) > 0 {
		length := make([]byte, 2)
		length[0] = uint8(len(extensions) >> 8)
		length[1] = uint8(len(extensions))
		extensions = append(length, extensions...)
	}
	hello := append(head, append(sessionID, append(ciphers, append(compressions, extensions...)...)...)...)
	hello[1] = uint8((len(hello) - 4) >> 16)
	hello[2] = uint8((len(hello) - 4) >> 8)
	hello[3] = uint8((len(hello) - 4))

	return hello, nil
}

func (c *Conn) clientHandshake() error {
	if c.config == nil {
		c.config = defaultConfig()
	}

	if len(c.config.ServerName) == 0 && !c.config.InsecureSkipVerify {
		return errors.New("tls: either ServerName or InsecureSkipVerify must be specified in the tls.Config")
	}

	c.handshakeLog = new(ServerHandshake)
	c.heartbleedLog = new(Heartbleed)

	var hello *clientHelloMsg
	var helloBytes []byte
	var session *ClientSessionState
	var cacheKey string
	var sessionCache ClientSessionCache

	if c.config.ClientFingerprint == nil {
		hello = &clientHelloMsg{
			vers:                 c.config.maxVersion(),
			compressionMethods:   []uint8{compressionNone},
			random:               make([]byte, 32),
			ocspStapling:         true,
			serverName:           c.config.ServerName,
			supportedCurves:      c.config.curvePreferences(),
			supportedPoints:      []uint8{pointFormatUncompressed},
			nextProtoNeg:         len(c.config.NextProtos) > 0,
			secureRenegotiation:  true,
			extendedMasterSecret: c.config.maxVersion() >= VersionTLS10 && c.config.ExtendedMasterSecret,
		}

		if c.config.ForceSessionTicketExt {
			hello.ticketSupported = true
		}
		if c.config.SignedCertificateTimestampExt {
			hello.sctEnabled = true
		}

		if c.config.HeartbeatEnabled && !c.config.ExtendedRandom {
			hello.heartbeatEnabled = true
			hello.heartbeatMode = heartbeatModePeerAllowed
		}

		possibleCipherSuites := c.config.cipherSuites()
		hello.cipherSuites = make([]uint16, 0, len(possibleCipherSuites))

		if c.config.ForceSuites {
			hello.cipherSuites = possibleCipherSuites
		} else {

		NextCipherSuite:
			for _, suiteId := range possibleCipherSuites {
				for _, suite := range implementedCipherSuites {
					if suite.id != suiteId {
						continue
					}
					// Don't advertise TLS 1.2-only cipher suites unless
					// we're attempting TLS 1.2.
					if hello.vers < VersionTLS12 && suite.flags&suiteTLS12 != 0 {
						continue
					}
					hello.cipherSuites = append(hello.cipherSuites, suiteId)
					continue NextCipherSuite
				}
			}
		}

		if len(c.config.ClientRandom) == 32 {
			copy(hello.random, c.config.ClientRandom)
		} else {
			_, err := io.ReadFull(c.config.rand(), hello.random)
			if err != nil {
				c.sendAlert(alertInternalError)
				return errors.New("tls: short read from Rand: " + err.Error())
			}
		}

		if c.config.ExtendedRandom {
			hello.extendedRandomEnabled = true
			hello.extendedRandom = make([]byte, 32)
			if _, err := io.ReadFull(c.config.rand(), hello.extendedRandom); err != nil {
				return errors.New("tls: short read from Rand: " + err.Error())
			}
		}

		if hello.vers >= VersionTLS12 {
			hello.signatureAndHashes = c.config.signatureAndHashesForClient()
		}

		sessionCache = c.config.ClientSessionCache
		if c.config.SessionTicketsDisabled {
			sessionCache = nil
		}
		if sessionCache != nil {
			hello.ticketSupported = true

			// Try to resume a previously negotiated TLS session, if
			// available.
			cacheKey = clientSessionCacheKey(c.conn.RemoteAddr(), c.config)
			candidateSession, ok := sessionCache.Get(cacheKey)
			if ok {
				// Check that the ciphersuite/version used for the
				// previous session are still valid.
				cipherSuiteOk := false
				for _, id := range hello.cipherSuites {
					if id == candidateSession.cipherSuite {
						cipherSuiteOk = true
						break
					}
				}

				versOk := candidateSession.vers >= c.config.minVersion() &&
					candidateSession.vers <= c.config.maxVersion()
				if versOk && cipherSuiteOk {
					session = candidateSession
				}
			}
		}

		if session != nil {
			hello.sessionTicket = session.sessionTicket
			// A random session ID is used to detect when the
			// server accepted the ticket and is resuming a session
			// (see RFC 5077).
			hello.sessionId = make([]byte, 16)
			if _, err := io.ReadFull(c.config.rand(), hello.sessionId); err != nil {
				c.sendAlert(alertInternalError)
				return errors.New("tls: short read from Rand: " + err.Error())
			}
		}

		helloBytes = hello.marshal()
	} else {
		session = nil
		sessionCache = nil
		var err error
		helloBytes, err = c.config.ClientFingerprint.marshal(c.config)
		if err != nil {
			return err
		}
		hello = &clientHelloMsg{}
		if ok := hello.unmarshal(helloBytes); !ok {
			return errors.New("Incompatible Custom Client Fingerprint")
		}
	}

	c.writeRecord(recordTypeHandshake, helloBytes)
	c.handshakeLog.ClientHello = hello.MakeLog()

	msg, err := c.readHandshake()
	if err != nil {
		return err
	}
	serverHello, ok := msg.(*serverHelloMsg)
	if !ok {
		c.sendAlert(alertUnexpectedMessage)
		return unexpectedMessageError(serverHello, msg)
	}
	c.handshakeLog.ServerHello = serverHello.MakeLog()

	if serverHello.heartbeatEnabled {
		c.heartbeat = true
		c.heartbleedLog.HeartbeatEnabled = true
	}

	vers, ok := c.config.mutualVersion(serverHello.vers)
	if !ok {
		c.sendAlert(alertProtocolVersion)
		return fmt.Errorf("tls: server selected unsupported protocol version %x", serverHello.vers)
	}
	c.vers = vers
	c.haveVers = true

	suite := mutualCipherSuite(c.config.cipherSuites(), serverHello.cipherSuite)
	cipherImplemented := cipherIDInCipherList(serverHello.cipherSuite, implementedCipherSuites)
	cipherShared := cipherIDInCipherIDList(serverHello.cipherSuite, c.config.cipherSuites())
	if suite == nil {
		//c.sendAlert(alertHandshakeFailure)
		if !cipherShared {
			c.cipherError = ErrNoMutualCipher
		} else if !cipherImplemented {
			c.cipherError = ErrUnimplementedCipher
		}
	}

	hs := &clientHandshakeState{
		c:            c,
		serverHello:  serverHello,
		hello:        hello,
		suite:        suite,
		finishedHash: newFinishedHash(c.vers, suite),
		session:      session,
	}

	hs.finishedHash.Write(hs.hello.marshal())
	hs.finishedHash.Write(hs.serverHello.marshal())

	isResume, err := hs.processServerHello()
	if err != nil {
		return err
	}

	if isResume {
		if c.cipherError != nil {
			c.sendAlert(alertHandshakeFailure)
			return c.cipherError
		}
		if err := hs.establishKeys(); err != nil {
			return err
		}
		if err := hs.readSessionTicket(); err != nil {
			return err
		}
		if err := hs.readFinished(); err != nil {
			return err
		}
		if err := hs.sendFinished(); err != nil {
			return err
		}
	} else {
		if err := hs.doFullHandshake(); err != nil {
			return err
		}
		if err := hs.establishKeys(); err != nil {
			return err
		}
		if err := hs.sendFinished(); err != nil {
			return err
		}
		if err := hs.readSessionTicket(); err != nil {
			return err
		}
		if err := hs.readFinished(); err != nil {
			return err
		}
	}

	if hs.session == nil {
		c.handshakeLog.SessionTicket = nil
	} else {
		c.handshakeLog.SessionTicket = hs.session.MakeLog()
	}

	c.handshakeLog.KeyMaterial = hs.MakeLog()

	if sessionCache != nil && hs.session != nil && session != hs.session {
		sessionCache.Put(cacheKey, hs.session)
	}

	c.didResume = isResume
	c.handshakeComplete = true
	c.cipherSuite = suite.id
	return nil
}

func (hs *clientHandshakeState) doFullHandshake() error {
	c := hs.c

	msg, err := c.readHandshake()
	if err != nil {
		return err
	}

	var serverCert *x509.Certificate

	isAnon := hs.suite != nil && (hs.suite.flags&suiteAnon > 0)

	if !isAnon {

		certMsg, ok := msg.(*certificateMsg)
		if !ok || len(certMsg.certificates) == 0 {
			c.sendAlert(alertUnexpectedMessage)
			return unexpectedMessageError(certMsg, msg)
		}
		hs.finishedHash.Write(certMsg.marshal())

		certs := make([]*x509.Certificate, len(certMsg.certificates))
		invalidCert := false
		var invalidCertErr error
		for i, asn1Data := range certMsg.certificates {
			cert, err := x509.ParseCertificate(asn1Data)
			if err != nil {
				invalidCert = true
				invalidCertErr = err
				break
			}
			certs[i] = cert
		}

		c.handshakeLog.ServerCertificates = certMsg.MakeLog()

		if !invalidCert {
			opts := x509.VerifyOptions{
				Roots:         c.config.RootCAs,
				CurrentTime:   c.config.time(),
				DNSName:       c.config.ServerName,
				Intermediates: x509.NewCertPool(),
			}

			// Always check validity of the certificates
			for _, cert := range certs {
				/*
					if i == 0 {
						continue
					}
				*/
				opts.Intermediates.AddCert(cert)
			}
			var validation *x509.Validation
			c.verifiedChains, validation, err = certs[0].ValidateWithStupidDetail(opts)
			c.handshakeLog.ServerCertificates.addParsed(certs, validation)

			// If actually verifying and invalid, reject
			if !c.config.InsecureSkipVerify {
				if err != nil {
					c.sendAlert(alertBadCertificate)
					return err
				}
			}
		}

		if invalidCert {
			c.sendAlert(alertBadCertificate)
			return errors.New("tls: failed to parse certificate from server: " + invalidCertErr.Error())
		}

		c.peerCertificates = certs

		if hs.serverHello.ocspStapling {
			msg, err = c.readHandshake()
			if err != nil {
				return err
			}
			cs, ok := msg.(*certificateStatusMsg)
			if !ok {
				c.sendAlert(alertUnexpectedMessage)
				return unexpectedMessageError(cs, msg)
			}
			hs.finishedHash.Write(cs.marshal())

			if cs.statusType == statusTypeOCSP {
				c.ocspResponse = cs.response
			}
		}

		serverCert = certs[0]

		var supportedCertKeyType bool
		switch serverCert.PublicKey.(type) {
		case *rsa.PublicKey, *ecdsa.PublicKey, *x509.AugmentedECDSA:
			supportedCertKeyType = true
			break
		case *dsa.PublicKey:
			if c.config.ClientDSAEnabled {
				supportedCertKeyType = true
			}
		default:
			break
		}

		if !supportedCertKeyType {
			c.sendAlert(alertUnsupportedCertificate)
			return fmt.Errorf("tls: server's certificate contains an unsupported type of public key: %T", serverCert.PublicKey)
		}

		msg, err = c.readHandshake()
		if err != nil {
			return err
		}
	}

	// If we don't support the cipher, quit before we need to read the hs.suite
	// variable
	if c.cipherError != nil {
		return c.cipherError
	}

	skx, ok := msg.(*serverKeyExchangeMsg)

	keyAgreement := hs.suite.ka(c.vers)

	if ok {
		hs.finishedHash.Write(skx.marshal())

		err = keyAgreement.processServerKeyExchange(c.config, hs.hello, hs.serverHello, serverCert, skx)
		c.handshakeLog.ServerKeyExchange = skx.MakeLog(keyAgreement)
		if err != nil {
			c.sendAlert(alertUnexpectedMessage)
			return err
		}

		msg, err = c.readHandshake()
		if err != nil {
			return err
		}
	}

	var chainToSend *Certificate
	var certRequested bool
	certReq, ok := msg.(*certificateRequestMsg)
	if ok {
		certRequested = true

		// RFC 4346 on the certificateAuthorities field:
		// A list of the distinguished names of acceptable certificate
		// authorities. These distinguished names may specify a desired
		// distinguished name for a root CA or for a subordinate CA;
		// thus, this message can be used to describe both known roots
		// and a desired authorization space. If the
		// certificate_authorities list is empty then the client MAY
		// send any certificate of the appropriate
		// ClientCertificateType, unless there is some external
		// arrangement to the contrary.

		hs.finishedHash.Write(certReq.marshal())

		var rsaAvail, ecdsaAvail bool
		for _, certType := range certReq.certificateTypes {
			switch certType {
			case certTypeRSASign:
				rsaAvail = true
			case certTypeECDSASign:
				ecdsaAvail = true
			}
		}

		// We need to search our list of client certs for one
		// where SignatureAlgorithm is RSA and the Issuer is in
		// certReq.certificateAuthorities
	findCert:
		for i, chain := range c.config.Certificates {
			if !rsaAvail && !ecdsaAvail {
				continue
			}

			for j, cert := range chain.Certificate {
				x509Cert := chain.Leaf
				// parse the certificate if this isn't the leaf
				// node, or if chain.Leaf was nil
				if j != 0 || x509Cert == nil {
					if x509Cert, err = x509.ParseCertificate(cert); err != nil {
						c.sendAlert(alertInternalError)
						return errors.New("tls: failed to parse client certificate #" + strconv.Itoa(i) + ": " + err.Error())
					}
				}

				switch {
				case rsaAvail && x509Cert.PublicKeyAlgorithm == x509.RSA:
				case ecdsaAvail && x509Cert.PublicKeyAlgorithm == x509.ECDSA:
				default:
					continue findCert
				}

				if len(certReq.certificateAuthorities) == 0 {
					// they gave us an empty list, so just take the
					// first RSA cert from c.config.Certificates
					chainToSend = &chain
					break findCert
				}

				for _, ca := range certReq.certificateAuthorities {
					if bytes.Equal(x509Cert.RawIssuer, ca) {
						chainToSend = &chain
						break findCert
					}
				}
			}
		}

		msg, err = c.readHandshake()
		if err != nil {
			return err
		}
	}

	shd, ok := msg.(*serverHelloDoneMsg)
	if !ok {
		c.sendAlert(alertUnexpectedMessage)
		return unexpectedMessageError(shd, msg)
	}
	hs.finishedHash.Write(shd.marshal())

	// If the server requested a certificate then we have to send a
	// Certificate message, even if it's empty because we don't have a
	// certificate to send.
	if certRequested {
		certMsg := new(certificateMsg)
		if chainToSend != nil {
			certMsg.certificates = chainToSend.Certificate
		}
		hs.finishedHash.Write(certMsg.marshal())
		c.writeRecord(recordTypeHandshake, certMsg.marshal())
	}

	preMasterSecret, ckx, err := keyAgreement.generateClientKeyExchange(c.config, hs.hello, serverCert)
	if err != nil {
		c.sendAlert(alertInternalError)
		return err
	}

	c.handshakeLog.ClientKeyExchange = ckx.MakeLog(keyAgreement)

	if ckx != nil {
		hs.finishedHash.Write(ckx.marshal())
		c.writeRecord(recordTypeHandshake, ckx.marshal())
	}

	if chainToSend != nil {
		var signed []byte
		certVerify := &certificateVerifyMsg{
			hasSignatureAndHash: c.vers >= VersionTLS12,
		}

		// Determine the hash to sign.
		var signatureType uint8
		switch c.config.Certificates[0].PrivateKey.(type) {
		case *ecdsa.PrivateKey:
			signatureType = signatureECDSA
		case *rsa.PrivateKey:
			signatureType = signatureRSA
		default:
			c.sendAlert(alertInternalError)
			return errors.New("unknown private key type")
		}
		certVerify.signatureAndHash, err = hs.finishedHash.selectClientCertSignatureAlgorithm(certReq.signatureAndHashes, c.config.signatureAndHashesForClient(), signatureType)
		if err != nil {
			c.sendAlert(alertInternalError)
			return err
		}
		digest, hashFunc, err := hs.finishedHash.hashForClientCertificate(certVerify.signatureAndHash, hs.masterSecret)
		if err != nil {
			c.sendAlert(alertInternalError)
			return err
		}

		switch key := c.config.Certificates[0].PrivateKey.(type) {
		case *ecdsa.PrivateKey:
			var r, s *big.Int
			r, s, err = ecdsa.Sign(c.config.rand(), key, digest)
			if err == nil {
				signed, err = asn1.Marshal(ecdsaSignature{r, s})
			}
		case *rsa.PrivateKey:
			signed, err = rsa.SignPKCS1v15(c.config.rand(), key, hashFunc, digest)
		default:
			err = errors.New("unknown private key type")
		}
		if err != nil {
			c.sendAlert(alertInternalError)
			return errors.New("tls: failed to sign handshake with client certificate: " + err.Error())
		}
		certVerify.signature = signed

		hs.writeClientHash(certVerify.marshal())
		c.writeRecord(recordTypeHandshake, certVerify.marshal())
	}

	var cr, sr []byte
	if hs.hello.extendedRandomEnabled {
		helloRandomLen := len(hs.hello.random)
		helloExtendedRandomLen := len(hs.hello.extendedRandom)

		cr = make([]byte, helloRandomLen+helloExtendedRandomLen)
		copy(cr, hs.hello.random)
		copy(cr[helloRandomLen:], hs.hello.extendedRandom)
	} else {
		cr = hs.hello.random
	}

	if hs.serverHello.extendedRandomEnabled {
		serverRandomLen := len(hs.serverHello.random)
		serverExtendedRandomLen := len(hs.serverHello.extendedRandom)

		sr = make([]byte, serverRandomLen+serverExtendedRandomLen)
		copy(sr, hs.serverHello.random)
		copy(sr[serverRandomLen:], hs.serverHello.extendedRandom)
	} else {
		sr = hs.serverHello.random
	}

	hs.preMasterSecret = make([]byte, len(preMasterSecret))
	copy(hs.preMasterSecret, preMasterSecret)

	if hs.serverHello.extendedMasterSecret && c.vers >= VersionTLS10 {
		hs.masterSecret = extendedMasterFromPreMasterSecret(c.vers, hs.suite, preMasterSecret, hs.finishedHash)
		c.extendedMasterSecret = true
	} else {
		hs.masterSecret = masterFromPreMasterSecret(c.vers, hs.suite, preMasterSecret, hs.hello.random, hs.serverHello.random)
	}

	return nil
}

func (hs *clientHandshakeState) establishKeys() error {
	c := hs.c

	clientMAC, serverMAC, clientKey, serverKey, clientIV, serverIV := keysFromMasterSecret(c.vers, hs.suite, hs.masterSecret, hs.hello.random, hs.serverHello.random, hs.suite.macLen, hs.suite.keyLen, hs.suite.ivLen)
	var clientCipher, serverCipher interface{}
	var clientHash, serverHash macFunction
	if hs.suite.cipher != nil {
		clientCipher = hs.suite.cipher(clientKey, clientIV, false /* not for reading */)
		clientHash = hs.suite.mac(c.vers, clientMAC)
		serverCipher = hs.suite.cipher(serverKey, serverIV, true /* for reading */)
		serverHash = hs.suite.mac(c.vers, serverMAC)
	} else {
		clientCipher = hs.suite.aead(clientKey, clientIV)
		serverCipher = hs.suite.aead(serverKey, serverIV)
	}

	c.in.prepareCipherSpec(c.vers, serverCipher, serverHash)
	c.out.prepareCipherSpec(c.vers, clientCipher, clientHash)
	return nil
}

func (hs *clientHandshakeState) serverResumedSession() bool {
	// If the server responded with the same sessionId then it means the
	// sessionTicket is being used to resume a TLS session.
	return hs.session != nil && hs.hello.sessionId != nil &&
		bytes.Equal(hs.serverHello.sessionId, hs.hello.sessionId)
}

func (hs *clientHandshakeState) processServerHello() (bool, error) {
	c := hs.c

	if hs.serverHello.compressionMethod != compressionNone {
		c.sendAlert(alertUnexpectedMessage)
		return false, errors.New("tls: server selected unsupported compression format")
	}

	if !hs.hello.nextProtoNeg && hs.serverHello.nextProtoNeg {
		c.sendAlert(alertHandshakeFailure)
		return false, errors.New("server advertised unrequested NPN extension")
	}

	if hs.serverResumedSession() {
		// Restore masterSecret and peerCerts from previous state
		hs.masterSecret = hs.session.masterSecret
		c.extendedMasterSecret = hs.session.extendedMasterSecret
		c.peerCertificates = hs.session.serverCertificates
		return true, nil
	}
	return false, nil
}

func (hs *clientHandshakeState) readFinished() error {
	c := hs.c

	c.readRecord(recordTypeChangeCipherSpec)
	if err := c.in.error(); err != nil {
		return err
	}

	msg, err := c.readHandshake()
	if err != nil {
		return err
	}
	serverFinished, ok := msg.(*finishedMsg)
	if !ok {
		c.sendAlert(alertUnexpectedMessage)
		return unexpectedMessageError(serverFinished, msg)
	}
	c.handshakeLog.ServerFinished = serverFinished.MakeLog()

	verify := hs.finishedHash.serverSum(hs.masterSecret)
	if len(verify) != len(serverFinished.verifyData) ||
		subtle.ConstantTimeCompare(verify, serverFinished.verifyData) != 1 {
		c.sendAlert(alertHandshakeFailure)
		return errors.New("tls: server's Finished message was incorrect")
	}
	hs.finishedHash.Write(serverFinished.marshal())
	return nil
}

func (hs *clientHandshakeState) readSessionTicket() error {
	if !hs.serverHello.ticketSupported {
		return nil
	}

	c := hs.c
	msg, err := c.readHandshake()
	if err != nil {
		return err
	}
	sessionTicketMsg, ok := msg.(*newSessionTicketMsg)
	if !ok {
		c.sendAlert(alertUnexpectedMessage)
		return unexpectedMessageError(sessionTicketMsg, msg)
	}
	hs.finishedHash.Write(sessionTicketMsg.marshal())

	hs.session = &ClientSessionState{
		sessionTicket:      sessionTicketMsg.ticket,
		vers:               c.vers,
		cipherSuite:        hs.suite.id,
		masterSecret:       hs.masterSecret,
		serverCertificates: c.peerCertificates,
		lifetimeHint:       sessionTicketMsg.lifetimeHint,
	}

	return nil
}

func (hs *clientHandshakeState) sendFinished() error {
	c := hs.c

	c.writeRecord(recordTypeChangeCipherSpec, []byte{1})
	if hs.serverHello.nextProtoNeg {
		nextProto := new(nextProtoMsg)
		proto, fallback := mutualProtocol(c.config.NextProtos, hs.serverHello.nextProtos)
		nextProto.proto = proto
		c.clientProtocol = proto
		c.clientProtocolFallback = fallback

		hs.finishedHash.Write(nextProto.marshal())
		c.writeRecord(recordTypeHandshake, nextProto.marshal())
	}

	finished := new(finishedMsg)
	finished.verifyData = hs.finishedHash.clientSum(hs.masterSecret)
	hs.finishedHash.Write(finished.marshal())

	c.handshakeLog.ClientFinished = finished.MakeLog()

	c.writeRecord(recordTypeHandshake, finished.marshal())
	return nil
}

func (hs *clientHandshakeState) writeClientHash(msg []byte) {
	// writeClientHash is called before writeRecord.
	hs.writeHash(msg, 0)
}

func (hs *clientHandshakeState) writeServerHash(msg []byte) {
	// writeServerHash is called after readHandshake.
	hs.writeHash(msg, 0)
}

func (hs *clientHandshakeState) writeHash(msg []byte, seqno uint16) {
	hs.finishedHash.Write(msg)
}

// clientSessionCacheKey returns a key used to cache sessionTickets that could
// be used to resume previously negotiated TLS sessions with a server.
func clientSessionCacheKey(serverAddr net.Addr, config *Config) string {
	if len(config.ServerName) > 0 {
		return config.ServerName
	}
	return serverAddr.String()
}

// mutualProtocol finds the mutual Next Protocol Negotiation protocol given the
// set of client and server supported protocols. The set of client supported
// protocols must not be empty. It returns the resulting protocol and flag
// indicating if the fallback case was reached.
func mutualProtocol(clientProtos, serverProtos []string) (string, bool) {
	for _, s := range serverProtos {
		for _, c := range clientProtos {
			if s == c {
				return s, false
			}
		}
	}

	return clientProtos[0], true
}
