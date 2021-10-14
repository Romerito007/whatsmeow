module go.mau.fi/whatsmeow

go 1.17

require (
	github.com/RadicalApp/libsignal-protocol-go v0.0.0-20170414202031-d09bcab9f18e
	github.com/gorilla/websocket v1.4.2
	github.com/mdp/qrterminal/v3 v3.0.0
	golang.org/x/crypto v0.0.0-20210921155107-089bfa567519
	google.golang.org/protobuf v1.27.1
	maunium.net/go/maulogger/v2 v2.2.4
)

require (
	filippo.io/edwards25519 v1.0.0-rc.1 // indirect
	github.com/RadicalApp/complete v0.0.0-20170329192659-17e6c0ee499b // indirect
	rsc.io/qr v0.2.0 // indirect
)

replace github.com/RadicalApp/libsignal-protocol-go => github.com/tulir/libsignal-protocol-go v0.0.0-20211014212652-fb541a37ef05
