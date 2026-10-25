Feature: keyryx's event types and agent-conform's chain_restarted are described honestly

  keyryx is the custody and witness service of the agent stack. It writes its
  own stream, keyryx.ndjson, with the source keyryx, and nine event types
  (agent-passport SPEC.md 6.2, registered as keyryx, RESERVED until its first
  release, @decided 2026-10-18; v0.1.0 was released the same day). agent-conform also writes chain_restarted
  (medium, high when no resume checkpoint explains it). Each has a sentence
  here that says what happened, what this box did and what happens if nobody
  acts. Severities are the producers' own and are not chosen here. Nothing a
  producer wrote as free text reaches a mail, and a name or an id is shown only
  when it passes a shape check (@claude 2026-10-18, the reading of the design
  in the security review's wave 2 notes).

  Scenario: every keyryx type and chain_restarted has a sentence
    Given a keyryx event of each of the nine types and a chain_restarted event
    When each is rendered
    Then none falls through to "raised an event this build does not have a description for"
  # @test:TestEveryKeyryxTypeIsDescribed

  Scenario: keyryx.ndjson may carry keyryx and nothing else
    Given a line claiming source keyryx inside keyryx.ndjson, and a line claiming source wardryx inside it
    When the watcher reads the file
    Then the keyryx line is processed and the wardryx line is refused and counted as foreign
  # @test:TestKeyryxStreamMayCarryKeyryxOnly

  Scenario: a witnessed rewrite names the stream and its counts, and never a hash
    Given a witness_stream_rewritten event for wardryx.ndjson with lines, bytes and a clipped prefix hash
    When it is rendered
    Then the mail names wardryx.ndjson, the line and byte counts, and says the bytes the witness had read changed, and neither hash string appears
  # @test:TestWitnessRewriteNamesTheStreamAndCountsNotHashes

  Scenario: a truncation names the stream and says lines were removed
    Given a witness_stream_truncated event for tokenfuse.ndjson
    When it is rendered
    Then the mail names tokenfuse.ndjson and says the stream holds fewer lines than when it was last read
  # @test:TestWitnessTruncationNamesTheStream

  Scenario: a witness finding about a tenant names the tenant
    Given a witness_checkpoint_invalid event whose data names a tenant and no stream
    When it is rendered
    Then the mail names the tenant
  # @test:TestWitnessFindingNamesATenant

  Scenario: a rotation names the new key, the previous key and the algorithm
    Given a key_rotated event with name, kid, previous_kid and alg
    When it is rendered
    Then the mail names all four and says nothing was revoked
  # @test:TestKeyRotatedNamesNewAndPreviousKid

  Scenario: a revocation names the key and says what to check
    Given a key_revoked event with name, kid and alg
    When it is rendered
    Then the mail names the key and says a revoked key is no longer published, and what to check
  # @test:TestKeyRevokedNamesTheKeyAndWhatToCheck

  Scenario: a chain restart names the stream and the line, and says what would explain it
    Given a chain_restarted event for wardryx.ndjson whose new chain starts at line 7
    When it is rendered
    Then the mail names wardryx.ndjson and line 7, and says a resume checkpoint explains a restart and a high event means none does
  # @test:TestChainRestartedNamesTheStreamAndLine

  Scenario: hostile values in keyryx events never reach the mail
    Given keyryx events whose names, kids and counts are sentences, paths, nested maps, negative, fractional or oversized
    When each is rendered
    Then none of that text is rendered and each mail still says what the type means
  # @test:TestKeyryxHostileValuesNeverReachTheMail

  Scenario: free text under keys nothing allowlists never reaches the mail
    Given a keyryx event that carries a note, a reason and a message written as prose
    When it is rendered
    Then none of the prose appears in the mail
  # @test:TestKeyryxFreeTextIsNeverMailed

  Scenario: keyryx's keys are not in the generic allowlist
    Given the allowlist of the generic per-key line
    Then none of name, kid, previous_kid, alg, lines, bytes, count, segments, stream or tenant is in it, so the generic line never prints them
  # @test:TestKeyryxKeysAreNotInTheGenericAllowlist
