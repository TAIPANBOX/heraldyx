Feature: An event is processed as the source it claims only when its file may carry that source

  The shared event bus is one directory with one file per writer, and every
  writer can append to every file. Nothing checked that the `source` inside a
  line is the source the file belongs to, so a line claiming `source: wardryx`
  inside tokenfuse.ndjson was rendered and mailed as a policy decision from
  wardryx. The owner chose that each writer writes only its own stream and
  everyone else reads, plus a verifier. This is the reader half for heraldyx:
  a line whose source is not allowed for the file it was read from is not
  processed as that source, it is counted beside the malformed lines, and one
  alert per file and claimed source says so. A file that legitimately carries
  more than one source, or a different name, is declared, so no legitimate
  event is dropped; a file nothing declares is read and said out loud once.

  Scenario: a line claiming another plane's name is not processed as that plane
    Given tokenfuse.ndjson holds a line that says it was raised by wardryx
    When heraldyx polls
    Then nothing is mailed about the line as a wardryx event, and exactly one alert says the claim was refused
  # @test:TestAForeignSourceIsNeverMailedAsThatSource

  Scenario: the alert names the file, the claim and the count, and nothing the refused lines said
    Given tokenfuse.ndjson holds three lines claiming wardryx
    When heraldyx polls
    Then the alert names the file, the claimed source, the count of three and what the file may carry, and carries no run or agent from the refused lines
  # @test:TestTheForeignSourceAlertNamesTheFileTheClaimAndTheCount

  Scenario: one alert per file and claimed source, however many polls
    Given a pair of file and claimed source was alerted on at one poll
    When more lines of the same pair arrive over later polls, outside the dedup window
    Then no further alert goes, while a different claim or a different file is a new pair and is alerted on once
  # @test:TestAForeignSourceIsRaisedOncePerFileAndClaimedSourceAcrossPolls

  Scenario: a restart does not alert again for a pair already alerted on
    Given a pair was alerted on and the process was restarted
    When another line of the same pair arrives
    Then no second alert goes
  # @test:TestAForeignSourceIsNotRaisedAgainAfterARestart

  Scenario: a legitimate line beside a refused one is still processed
    Given tokenfuse.ndjson holds a tokenfuse line and a line claiming wardryx
    When heraldyx polls
    Then the tokenfuse line is mailed as an alert and the other is refused with one alert about the refusal
  # @test:TestALegitimateLineBesideAForeignOneIsStillMailed

  Scenario: a file nobody declared cannot carry several sources, even one named demo
    Given a co-tenant creates demo.ndjson holding a line claiming wardryx
    When heraldyx polls with no declaration
    Then the line is not processed as wardryx and one alert names demo.ndjson
  # @test:TestACoTenantsDemoFileIsRefusedByDefault

  Scenario: a file the operator declared to carry several sources is processed whole
    Given HERALDYX_STREAMS declares demo=tokenfuse|wardryx|mockryx and demo.ndjson holds lines from those three
    When heraldyx polls
    Then all three are mailed as their own sources and nothing is raised about the file
  # @test:TestADeclaredMultiSourceFileIsProcessedWholeAndRaisesNothing

  Scenario: agent-conform's own stream is a known stream
    Given agent-conform.ndjson holds a line claiming agent-conform
    When the stream rule decides it
    Then it is allowed as a known single-source stream and not counted as an unknown stream
  # @test:TestAgentConformIsAKnownSingleSourceStream

  Scenario: the control plane's and the broker's own files carry the tokenfuse source
    Given tokenfuse-cloud.ndjson and tokenfuse-mcp.ndjson each hold a tokenfuse line
    When heraldyx polls
    Then both are mailed and nothing is raised
  # @test:TestARenamedFileOfTheSameProducerIsProcessed

  Scenario: a stream nothing declares is read and said out loud once
    Given newplane.ndjson, which nothing declares, holds lines claiming newplane
    When heraldyx polls over several polls
    Then every line is mailed as an alert, and one alert says the box does not know the stream
  # @test:TestAnUnknownStreamIsReadAndSaidOnceNotTrustedInSilence

  Scenario: an undeclared stream claiming another plane's name is the forged case
    Given newplane.ndjson holds a line claiming tokenfuse
    When heraldyx polls
    Then the line is not processed as tokenfuse and one alert says the claim was refused
  # @test:TestAnUnknownStreamClaimingAnotherSourceIsRefused

  Scenario: a refusal below the operator's floor goes to the daily summary
    Given the alert floor is critical
    When tokenfuse.ndjson holds a line claiming wardryx
    Then nothing is mailed now and the daily summary counts the refusal, not an event of the claimed source
  # @test:TestARefusalBelowTheFloorGoesToTheDigest

  Scenario: hostile lines are unchanged and a hostile claim cannot break the mail
    Given tokenfuse.ndjson holds garbage, a truncated envelope, a claimed source with a header injection, and one legitimate line
    When heraldyx polls
    Then the legitimate line is mailed, the injection reaches no header, and the hostile claim is refused
  # @test:TestHostileLinesAreUnchangedAndAHostileClaimCannotBreakTheMail

  Scenario: an operator declares what a file of their own may carry
    Given events.ndjson holds tokenfuse and wardryx lines
    When it is undeclared the lines are refused, and when HERALDYX_STREAMS declares events=tokenfuse|wardryx they are mailed with nothing raised
    Then the declaration is the only thing that changes the outcome
  # @test:TestAFileNamedEventsIsRefusedUntilItsSourcesAreDeclared
