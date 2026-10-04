Feature: agent-conform's two event types name the stream, the line and what to check

  agent-conform is the on-box hash-chain verifier (agent-passport SPEC.md 6.2),
  an optional add-on that reads the shared bus and writes only its own stream.
  heraldyx describes its two types, chain_broken (high) and chain_unchained
  (low), instead of mailing them as the generic fallback, which names no
  stream and no line (@claude 2026-10-04, asked for by that day's audit plan). Severities are agent-conform's own and are
  not changed here. A break is evidence, not a verdict: the stream was edited
  or two writers interleaved, and the verifier does not say which. An unchained
  stream is not a break; what it costs is that an edit would not be seen.

  Scenario: both types have a sentence
    Given chain_broken and chain_unchained events as agent-conform writes them
    When each is rendered
    Then neither falls through to "raised an event this build does not have a description for"
  # @test:TestEveryAgentConformTypeIsDescribed

  Scenario: a broken chain names the stream, the first broken line and how many breaks
    Given a chain_broken event for wardryx.ndjson, first break at line 12, 3 breaks
    Then the subject and the body name wardryx.ndjson, the body says line 12 and 3 breaks and the kind, and neither hash string the verifier clipped appears
  # @test:TestChainBrokenNamesTheStreamTheFirstBrokenLineAndTheBreakCount

  Scenario: one break reads as one break
    Given a chain_broken event with a single break at line 1
    Then the mail says "1 break in all" and names line 1
  # @test:TestChainBrokenCountsASingleBreakInTheSingular

  Scenario: a break is evidence, and the mail says what to check
    Given a chain_broken event
    Then the mail calls it tamper-evidence, names editing and interleaved writers as the two causes, says the verifier does not say which, and points at agent-conform.ndjson and the pod or service log
  # @test:TestChainBrokenSaysWhatItIsEvidenceOfAndWhatToCheck

  Scenario: hostile values in a chain_broken event never reach the mail
    Given chain_broken events whose file name holds a line break, is 5000 characters, is unicode or a path or a sentence, and whose line and break count are text, huge, negative, fractional or nested
    Then none of it is rendered and the mail still says which check broke
  # @test:TestChainBrokenWithHostileValuesStaysSafe

  Scenario: a file name that cannot be printed is said out loud
    Given a chain_broken event whose file name is not one this box can print as written
    Then the mail says the file name is left out and still gives the line and the count
  # @test:TestChainBrokenSaysWhenTheFileNameCannotBePrinted

  Scenario: a hostile kind is dropped
    Given a chain_broken event whose kind carries a line break and a forged header
    Then the kind does not reach the mail
  # @test:TestChainBrokenKindIsRenderedOnlyThroughTheShapeCheck

  Scenario: an unchained stream is named and is not called a break
    Given a chain_unchained event for engram.ndjson with 40 events
    Then the mail names the stream and the 40 events, says it is not a break, says an edit would not be detected, and does not call it tamper-evidence
  # @test:TestChainUnchainedNamesTheStreamAndTheEventCountAndSaysItIsNotABreak

  Scenario: hostile values in a chain_unchained event never reach the mail
    Given chain_unchained events with a hostile file name or an event count that is text, huge or negative
    Then none of it is rendered
  # @test:TestChainUnchainedWithHostileValuesStaysSafe

  Scenario: another plane's file and line keys are not made renderable
    Given a budget_threshold event whose data carries file, line, breaks and events
    Then none of them is rendered
  # @test:TestFileLineBreaksAndEventsAreRenderedOnlyForAgentConformTypes

  Scenario: the generic allowlist did not grow
    Given the data allowlist
    Then file, line, breaks, events and the verifier's other fields are not in it
  # @test:TestAgentConformKeysAreNotInTheGenericAllowlist

  Scenario: the severities are agent-conform's own
    Given chain_broken at high and chain_unchained at low
    Then both still render as described types
  # @test:TestAgentConformSeveritiesAreNotChangedByThisFile
