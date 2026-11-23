package render

import "github.com/TAIPANBOX/agent-stack-go/v2/event"

// vouchryxQualify adjusts the three vouchryx sentences off values vouchryx
// writes from closed sets of its own: `grant` on an issue, `reason` on a
// refusal, `ban` and `durable` on a revocation. Every sentence is this file's
// own prose. No value is printed, so a reason code vouchryx adds later, or a
// sentence written where a code belongs, keeps the base wording and reaches no
// mailbox, the same fallback [typedRefused] uses.
func vouchryxQualify(e event.Event, p phrasing) phrasing {
	switch e.Type {
	case "delegation_issued":
		return delegationIssued(e, p)
	case "delegation_denied":
		return delegationDenied(e, p)
	case "delegation_revoked":
		return delegationRevoked(e, p)
	}
	return p
}

// delegationIssued says which grant the token came from. The RFC 8693 token
// exchange writes no `grant` and carries the subject's issuer instead; the
// agent's own token writes `client_credentials`, and the cross-app grant
// writes `id-jag`.
func delegationIssued(e event.Event, p phrasing) phrasing {
	grant, hasGrant := e.Data["grant"].(string)
	switch {
	case hasGrant && grant == "client_credentials":
		p.what = "was issued a token of its own by vouchryx"
		p.did = "Nothing. The agent authenticated as itself and vouchryx issued it a short-lived token in its own name, bound to the key it proved it holds. No one delegated anything to it."
	case hasGrant && grant == "id-jag":
		p.what = "was issued a cross-app access token by vouchryx"
		p.did = "Nothing. A registered client presented an identity assertion from a configured identity provider, and vouchryx issued a short-lived token for one resource on behalf of the person it names."
	case !hasGrant:
		if _, ok := e.Data["subject_issuer"].(string); ok {
			p.what = "was issued a token to act on someone's behalf by vouchryx"
			p.did = "Nothing. vouchryx exchanged a token the agent presented for a short-lived one that lets it act on behalf of the token's subject, bound to the key the agent proved it holds."
			if unbound, _ := e.Data["subject_unbound"].(bool); unbound {
				p.did += " The token it presented carried no key of its own, from an issuer configured as one that sends such tokens; the new token is still bound to the agent's key."
			}
		}
	}
	return p
}

// delegationDenied says which kind of check refused the request, off
// vouchryx's `reason` codes (read from every deny call in internal/api).
func delegationDenied(e event.Event, p phrasing) phrasing {
	reason, _ := e.Data["reason"].(string)
	switch reason {
	case "agent_banned", "party_banned":
		p.what = "was refused a token by vouchryx because it, or a party in its chain, is banned"
		p.did = "vouchryx refused the request and issued nothing: a ban an operator placed is in force."
		p.next = "Nothing automatic. The ban holds until an operator lifts it. If this agent should be working, check who banned it and why in vouchryx's revocation store."
	case "subject_revoked", "actor_revoked":
		p.what = "presented a revoked token to vouchryx and was refused"
		p.did = "vouchryx refused the request and issued nothing: a token offered in the exchange is on the revocation list."
		p.next = "Nothing automatic. Something still holds a revoked token and is offering it. If the revocation answered a compromise, find what presented it."
	case "dpop_required", "no_dpop_proof", "bad_dpop_proof", "actor_key_mismatch", "subject_key_mismatch",
		"actor_credential_unbound", "subject_unbound":
		p.what = "asked vouchryx for a token without proving it holds the key the token is bound to"
		p.did = "vouchryx refused the request and issued nothing: it carried no valid proof of possession, or a token in it is not bound to the key that made the proof."
		p.next = "Nothing automatic. A client that never sends a proof is misconfigured. A proof made with the wrong key is a token being offered by something other than its holder. Check vouchryx's log for this agent."
	case "scope_widened":
		p.what = "asked vouchryx for more scope than the token it presented holds"
		p.did = "vouchryx refused the request and issued nothing: a delegation can only narrow what its subject holds."
		p.next = "Nothing automatic. A caller that keeps asking for more than it was given is a misconfigured client or an agent reaching past its grant. Check what it asked for in vouchryx's log."
	case "audience_not_allowed", "one_audience_only", "resource_not_configured":
		p.what = "asked vouchryx for a token for an audience it does not issue for"
		p.did = "vouchryx refused the request and issued nothing: the audience or resource asked for is not on this service's allow-list, or more than one was asked for at once."
		p.next = "Nothing automatic. Either the allow-list is missing a service this agent legitimately calls, or the agent is asking for access somewhere it was not meant to reach."
	}
	return p
}

// delegationRevoked says whether the revocation is also a ban, and whether it
// reached disk. A revocation vouchryx could not write is in force in the
// running process only (vouchryx answers 503 to the revoker, and keeps it).
func delegationRevoked(e event.Event, p phrasing) phrasing {
	if ban, _ := e.Data["ban"].(bool); ban {
		p.what = "was revoked and banned in vouchryx"
		p.did += " It is also a ban: vouchryx issues nothing that names this agent until an operator lifts it, and lifting it never restores what was revoked."
	}
	if durable, ok := e.Data["durable"].(bool); ok && !durable {
		p.next = "Act now: the revocation was not written to disk. It is in force in the running vouchryx only, a restart of vouchryx would lose it, and the revoker was told it failed. Revoke again once vouchryx's revocation store is writable. Then check that it was planned, and if it answered a compromise, what this agent did before it."
	}
	return p
}
