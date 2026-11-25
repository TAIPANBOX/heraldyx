# The checkpoint gate's custody mode: each checkpoint's key comes from the key
# statement keyryx custody publishes into a directory this process mounts read
# only, verified under a delegation root this box pinned. Each scenario names
# its test on the line after its last step; scripts/features-are-bound.sh holds
# the binding both ways.

Feature: a checkpoint key is trusted only on keyryx custody's published word, under a pinned root

  Scenario: keyryx's own statement vectors give keyryx's answers here
    Given the statement vectors keyryx writes and commits: its root, its pin, two valid statements and nine variants
    When this box verifies each under the pinned root
    Then each valid one verifies and certifies the key keyryx says, and each variant is refused
  # @test:TestKeyryxsStatementVectorsGiveKeyryxsAnswers

  Scenario: a statement verifies only under the root this box pinned
    Given a pin that names one half only, or a half the published root set does not carry
    When the pin is read and a statement verified under it
    Then a pin without both halves is refused, the missing kid is named, and the statement does not verify
  # @test:TestAStatementVerifiesOnlyUnderThePinnedRoot

  Scenario: a malformed pin or root key set is refused
    Given pins that are empty, not kids, too short or repeated, and root key sets whose keys name another kid, are of another kind or carry an unknown member
    When each is read
    Then each is refused
  # @test:TestMalformedPinsAndRootSetsAreRefused

  Scenario: hostile statement bytes never verify and never panic
    Given a valid statement with bytes changed at random, two thousand times, and empty, null and oversized inputs
    When each is verified
    Then none verifies and none panics
  # @test:TestHostileStatementBytesNeverVerifyAndNeverPanic

  Scenario: a certified key is found once its file is read
    Given a key custody certified and published for the stream's key name
    When the gate asks before and after the resolver's loop read its file
    Then it waits first and finds the key after
  # @test:TestACertifiedKeyIsFoundOnceItsFileIsRead

  Scenario: a key certified for another name, or a stream bound to none, is not certified
    Given a key certified for another producer's key name, a stream bound to no name, and a kid that is a path
    When the gate asks about each
    Then none is certified for this stream, and the key covers its own stream
  # @test:TestAKeyCertifiedForAnotherNameDoesNotCoverThisStream

  Scenario: a record outside its key's life is not certified
    Given records at not_before, a second before, the last second, not_after and a day after
    When the gate asks again about each, and again after the key ended
    Then only the records inside the key's life are certified, and they stay certified after it ended
  # @test:TestARecordOutsideItsKeysLifeIsNotCertified

  Scenario: a record is taken only while its key is alive and not dated ahead
    Given a certified key, records dated inside and past the skew ahead of this box's clock, and records read for the first time after the key ended
    When the gate reads each for the first time, and asks again about one taken earlier
    Then a record dated ahead or first read after its key ended is not taken, and one taken earlier stays covered
  # @test:TestARecordIsTakenOnlyWhileItsKeyIsAliveAndNotDatedAhead

  Scenario: a revocation uncovers on the next read and stays
    Given a certified key that custody then publishes as revoked, and later without the revocation
    When the resolver reads its file again
    Then the key is not certified, a backdated record does not bring it back, and the revocation is not taken back
  # @test:TestARevocationUncoversOnTheNextReadAndStays

  Scenario: a revocation set for later still covers until its time
    Given a key custody revoked as of an hour from now
    When the gate asks now and in an hour
    Then it is certified now and not after
  # @test:TestARevocationNotYetInForceStillCovers

  Scenario: a kid with no file waits a while and then is not certified
    Given a kid custody publishes a moment after it is asked about, and a kid it never publishes
    When the gate asks, and again after two minutes
    Then the first waits and is then found, and the second is not certified once its wait is over
  # @test:TestAKidWithNoFileWaitsAWhileAndThenIsNotCertified

  Scenario: a root half custody no longer publishes verifies nothing
    Given a statement verified under the pinned root
    When custody's published root key set no longer carries the pinned kids
    Then the statement no longer verifies and the missing pin is said
  # @test:TestARootHalfCustodyNoLongerPublishesVerifiesNothing

  Scenario: a statement under another root is not certified
    Given custody's directory holding a statement signed by a root this box did not pin
    When the gate asks about its kid
    Then the key is not certified and the log says the statement does not verify
  # @test:TestAStatementUnderAnotherRootIsNotCertified

  Scenario: only regular files of custody's shape are read
    Given a kid's file that is a link to a valid statement, then one with a member custody never writes, then one over its size cap
    When the resolver reads each
    Then none is certified
  # @test:TestOnlyRegularFilesOfCustodysShapeAreRead

  Scenario: a checkpoint file naming kid after kid cannot fill the resolver
    Given records naming as many unknown kids as the resolver remembers, then another stream's real key
    When the gate asks, and again once the unknown kids are forgotten
    Then the real key waits rather than being refused, and is found once there is room
  # @test:TestAFileNamingKidAfterKidCannotFillTheResolver

  Scenario: the statements directory and the bindings are read strictly
    Given directories that are relative, not clean, a link, a file, writable by others, missing or this process's own, and bindings that are not stream|key-name
    When each is read
    Then each is refused, and good bindings are read as written
  # @test:TestTheStatementsDirectoryAndBindingsAreReadStrictly

  Scenario: a statement whose post-quantum half is classical is not certified
    Given a statement whose ML-DSA-65 half is an ES384 signature by the root's classical key, naming that key as both halves
    When the gate asks about its kid
    Then the key is not certified, and the same claims with both real halves are
  # @test:TestAStatementWhosePostQuantumHalfIsClassicalIsNotCertified
