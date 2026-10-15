Feature: An operator acknowledges one condition, and a critical is never held

  @decided 2026-10-15: an operator who already knows about a condition can
  stop the repeat mails about it for a bounded time with a command on the box.
  Mail stays free of controls and the process speaks no HTTP, so the
  acknowledgement is a local command, authenticated by access to the box. It
  holds alerts below critical only, never longer than seven days, and what it
  held is mailed when it ends. A critical is sent whatever is acknowledged.

  Scenario: an acknowledged condition keeps tripping
    Given the operator acknowledged a condition for one hour
    When it trips twice in that hour and the hour ends
    Then neither alert is mailed, one mail says two were held, and the next one is mailed as usual
  # @test:TestAnAckHoldsItsAlertsAndTheirCountIsMailedWhenItEnds

  Scenario: a critical arrives for an acknowledged condition
    Given the operator acknowledged a condition
    When a critical arrives for it
    Then it is mailed at once and says it was sent despite the acknowledgement
  # @test:TestACriticalIsSentWhileItsKeyIsAcknowledged

  Scenario: an acknowledgement longer than seven days
    When the operator asks for 169 hours
    Then the command refuses and nothing is written
  # @test:TestAnAckLongerThanSevenDaysIsRefused

  Scenario: the running process and the command share the box
    Given an acknowledgement has ended
    When the process polls
    Then it does not rewrite the file the command owns
  # @test:TestTheLoopNeverWritesTheControlFile

  Scenario: an acknowledgement covers only its own key
    Given run-1 is acknowledged
    When run-10 trips
    Then run-10 is mailed
  # @test:TestAnAckHoldsOnlyItsOwnKey
