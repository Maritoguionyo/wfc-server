package qr2

import (
	"github.com/logrusorgru/aurora/v3"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"wwfc/common"
	"wwfc/logging"
)

const (
	ClientNoEndian = iota
	ClientBigEndian
	ClientLittleEndian
)

type Session struct {
	SessionID     uint32
	SearchID      uint64
	Addr          net.Addr
	Challenge     string
	Authenticated bool
	LastKeepAlive int64
	Endianness    byte // Some fields depend on the client's endianness
	Data          map[string]string
	PacketCount   uint32
	ReservationID uint64
	GroupPointer  *Group
}

var (
	sessions          = map[uint64]*Session{}
	sessionBySearchID = map[uint64]*Session{}
	mutex             = sync.RWMutex{}
)

// Remove a session.
func removeSession(addr uint64) {
	mutex.Lock()
	defer mutex.Unlock()

	session := sessions[addr]
	if session == nil {
		return
	}

	if session.GroupPointer != nil {
		delete(session.GroupPointer.Players, session)

		if len(session.GroupPointer.Players) == 0 {
			logging.Notice("QR2", "Deleting group", aurora.Cyan(session.GroupPointer.GroupName))
			delete(groups, session.GroupPointer.GroupName)
		} else if session.GroupPointer.Server == session {
			logging.Notice("QR2", "Server down in group", aurora.Cyan(session.GroupPointer.GroupName))
			session.GroupPointer.Server = nil
			// TODO: Search for new host via dwc_hoststate
		}
	}

	// Delete search ID lookup
	delete(sessionBySearchID, sessions[addr].SearchID)

	delete(sessions, addr)
}

// Update session data, creating the session if it doesn't exist. Returns a copy of the session data.
func setSessionData(moduleName string, addr net.Addr, sessionId uint32, payload map[string]string) (Session, bool) {
	newPID, newPIDValid := payload["dwc_pid"]
	delete(payload, "dwc_pid")

	lookupAddr := makeLookupAddr(addr.String())

	// Moving into performing operations on the session data, so lock the mutex
	mutex.Lock()
	defer mutex.Unlock()
	session, sessionExists := sessions[lookupAddr]

	if sessionExists && session.Addr.String() != addr.String() {
		logging.Error(moduleName, "Session IP mismatch")
		return Session{}, false
	}

	if !sessionExists {
		session = &Session{
			SessionID:     sessionId,
			Addr:          addr,
			Challenge:     "",
			Authenticated: false,
			LastKeepAlive: time.Now().Unix(),
			Endianness:    ClientNoEndian,
			Data:          payload,
			PacketCount:   0,
			ReservationID: 0,
		}
	}

	if newPIDValid && !session.setProfileID(moduleName, newPID) {
		return Session{}, false
	}

	if !sessionExists {
		logging.Notice(moduleName, "Creating session", aurora.Cyan(sessionId).String())

		// Set search ID
		for {
			searchID := uint64(rand.Int63n((0x400<<32)-1) + 1)
			if _, exists := sessionBySearchID[searchID]; !exists {
				session.SearchID = searchID
				session.Data["+searchid"] = strconv.FormatUint(searchID, 10)
				sessionBySearchID[searchID] = session
				break
			}
		}

		sessions[lookupAddr] = session
		return *session, true
	}

	// Save certain fields
	for k, v := range session.Data {
		if k[0] == '+' || k == "dwc_pid" {
			payload[k] = v
		}
	}

	session.Data = payload
	session.LastKeepAlive = time.Now().Unix()
	session.SessionID = sessionId
	return *session, true
}

// Set the session's profile ID if it doesn't already exists.
// Returns false if the profile ID is invalid.
// Expects the global mutex to already be locked.
func (session *Session) setProfileID(moduleName string, newPID string) bool {
	if oldPID, oldPIDValid := session.Data["dwc_pid"]; oldPIDValid && oldPID != "" {
		if newPID != oldPID {
			logging.Error(moduleName, "New dwc_pid mismatch: new:", aurora.Cyan(newPID), "old:", aurora.Cyan(oldPID))
			return false
		}

		return true
	}

	// Setting a new PID so validate it
	profileID, err := strconv.ParseUint(newPID, 10, 32)
	if err != nil || strconv.FormatUint(profileID, 10) != newPID {
		logging.Error(moduleName, "Invalid dwc_pid value:", aurora.Cyan(newPID))
		return false
	}

	// Check if the public IP matches the one used for the GPCM session
	var gpPublicIP string
	if loginInfo, ok := logins[uint32(profileID)]; ok {
		gpPublicIP = loginInfo.GPPublicIP
	} else {
		logging.Error(moduleName, "Provided dwc_pid is not logged in:", aurora.Cyan(newPID))
		return false
	}

	if strings.Split(gpPublicIP, ":")[0] != strings.Split(session.Addr.String(), ":")[0] {
		logging.Error(moduleName, "Caller public IP does not match GPCM session")
		return false
	}

	// Constraint: only one session can exist with a profile ID
	var outdated []uint64
	for sessionAddr, otherSession := range sessions {
		if otherSession == session {
			continue
		}

		if otherPID, ok := otherSession.Data["dwc_pid"]; !ok || otherPID != newPID {
			continue
		}

		// Remove old sessions with the PID
		outdated = append(outdated, sessionAddr)
	}

	for _, sessionAddr := range outdated {
		logging.Notice(moduleName, "Removing outdated session", aurora.BrightCyan(sessions[sessionAddr].Addr.String()), "with PID", aurora.Cyan(newPID))
		removeSession(sessionAddr)
	}

	session.Data["dwc_pid"] = newPID
	logging.Notice(moduleName, "Opened session with PID", aurora.Cyan(newPID))

	return true
}

func makeLookupAddr(addr string) uint64 {
	ip, port := common.IPFormatToInt(addr)
	return (uint64(port) << 32) | uint64(uint32(ip))
}

// Get a copy of the list of servers
func GetSessionServers() []map[string]string {
	var servers []map[string]string
	var unreachable []uint64
	currentTime := time.Now().Unix()

	mutex.Lock()
	defer mutex.Unlock()
	for sessionAddr, session := range sessions {
		// If the last keep alive was over a minute ago then consider the server unreachable
		if session.LastKeepAlive < currentTime-60 {
			// If the last keep alive was over an hour ago then remove the server
			if session.LastKeepAlive < currentTime-((60*60)*1) {
				unreachable = append(unreachable, sessionAddr)
			}
			continue
		}

		if !session.Authenticated {
			continue
		}

		servers = append(servers, session.Data)
	}

	// Remove unreachable sessions
	for _, sessionAddr := range unreachable {
		logging.Notice("QR2", "Removing unreachable session", aurora.BrightCyan(sessions[sessionAddr].Addr.String()))
		removeSession(sessionAddr)
	}

	return servers
}
