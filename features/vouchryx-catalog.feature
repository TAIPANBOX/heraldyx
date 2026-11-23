Feature: Vouchryx's delegation events say what happened in this box's own words

  Vouchryx is the delegation service: it issues short-lived tokens to agents,
  refuses the requests it will not honour, and revokes authority when an
  operator asks. agent-passport SPEC 6.2 registers its three types. Until now
  they mailed as an event this build has no description for. The sentences are
  read against the code that writes the events, and every value they turn on
  is a closed set of the producer's own words, selected and never printed.

  Scenario: each of vouchryx's three types has a description
    Given a delegation_issued, a delegation_denied and a delegation_revoked event filed under an agent
    When each is rendered
    Then none of them falls back to the generic description
  # @test:TestEveryVouchryxTypeIsDescribed

  Scenario: a refusal says which kind of check refused it, never the raw reason
    Given delegation_denied events whose reason is a ban, a revoked token, a missing proof of possession, a widened scope or an audience not on the allow-list
    When each is rendered
    Then the subject says which kind of refusal it was in this box's words, and the producer's reason code is not in the mail
  # @test:TestADelegationDenialSaysWhichKindOfRefusal

  Scenario: a refusal reason this build does not know keeps the neutral wording
    Given a delegation_denied event whose reason is a code this build has not learned, or a sentence
    When it is rendered
    Then the mail keeps the base wording and the reason is not in it
  # @test:TestAnUnknownDenialReasonKeepsTheBaseWording

  Scenario: what vouchryx keeps for its operator is never mailed
    Given a delegation_denied event carrying an error detail, the scopes asked for and held, and an audience
    When it is rendered
    Then none of those values is in the mail
  # @test:TestVouchryxDetailIsNeverMailed

  Scenario: a revocation that could not be written to disk says a restart would lose it
    Given a delegation_revoked event marked not durable
    When it is rendered
    Then the mail says the revocation is in force only in the running service and a restart would lose it
  # @test:TestARevocationThatIsNotDurableSaysARestartLosesIt

  Scenario: a revocation that is also a ban says nothing names the agent until it is lifted
    Given a delegation_revoked event with ban set
    When it is rendered
    Then the subject says the agent was banned and the mail says the ban holds until an operator lifts it
  # @test:TestARevocationThatIsABanSaysSo

  Scenario: an issue says which grant it was
    Given delegation_issued events for an agent's own token, a cross-app access token and a token exchange
    When each is rendered
    Then the subject tells the three apart
  # @test:TestAnIssueSaysWhichGrantItWas
