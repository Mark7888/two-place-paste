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
