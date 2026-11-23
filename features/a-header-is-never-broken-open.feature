Feature: No transport writes a header a producer or a setting can break open

  A subject line is built from ids a producer chose, and the recipients and the
  sender come from settings. The mail transport refused a line break in any of
  them before it wrote a header; the file transport wrote the same headers with
  no check at all. Both transports now go through one check, and that check
  refuses every way a value can end a header line, not only the two ASCII ones.

  Scenario: the file transport is handed a subject that would add a header
    Given a message whose subject holds a carriage return, a line feed, a NUL, a next-line, a line separator or a paragraph separator
    When the file transport is asked to write it
    Then it refuses, and the file gains no header line it did not have
  # @test:TestTheFileTransportRefusesAHeaderABreakWouldOpen

  Scenario: the file transport is handed a recipient that would add a header
    Given a recipient address that holds any of those characters
    When the file transport is asked to write a message to it
    Then it refuses, and nothing is written
  # @test:TestTheFileTransportRefusesARecipientABreakWouldOpen

  Scenario: the mail transport refuses the same characters in every header
    Given a sender, a recipient or a subject that holds any of those characters
    When the mail transport composes the message
    Then it refuses to build it
  # @test:TestEveryLineBreakAndControlIsRefusedInEveryHeader

  Scenario: both transports judge a value the same way
    Given a table of hostile values and a table of ordinary ones, a tab and a non-ASCII subject among the ordinary
    When each value goes to the file transport and to the mail transport
    Then both refuse every hostile value and both accept every ordinary one
  # @test:TestBothTransportsJudgeAHeaderTheSameWay
