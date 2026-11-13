package ws

// Test seams. They exist so that ws_test can assert on two facts that have no
// place in the wire contract: the offer-minting limit, and whether the server
// still holds a route to an offering device.

// OffersPerConn is the per-connection cap on pairing offers.
const OffersPerConn = offersPerConn

// WaitingOffers is how many offering devices the hub can still reach.
func (s *Server) WaitingOffers() int {
	s.hub.mu.RLock()
	defer s.hub.mu.RUnlock()
	return len(s.hub.offers)
}

// SetProtocolMin raises the lowest protocol level the server accepts. The
// shipped minimum is 1, so without it the refusal path could not be reached.
func (s *Server) SetProtocolMin(level uint32) {
	s.protocolMin.Store(level)
}

// SetAfterPairingNotice installs a hook that runs on the joiner's goroutine
// immediately after the join notice has been queued for the inviter. It lets a
// test occupy the window in which the inviter can answer, without sleeping.
func (s *Server) SetAfterPairingNotice(fn func()) {
	s.afterPairingNotice = fn
}
