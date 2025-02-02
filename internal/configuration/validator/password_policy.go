package validator

import (
	"fmt"

	"github.com/authelia/authelia/v4/internal/configuration/schema"
	"github.com/authelia/authelia/v4/internal/utils"
)

// ValidatePasswordPolicy validates and update Password Policy configuration.
func ValidatePasswordPolicy(config *schema.PasswordPolicyConfiguration, validator *schema.StructValidator) {
	if !utils.IsBoolCountLessThanN(1, true, config.Standard.Enabled, config.ZXCVBN.Enabled) {
		validator.Push(fmt.Errorf(errPasswordPolicyMultipleDefined))
	}

	if config.Standard.Enabled {
		if config.Standard.MinLength == 0 {
			config.Standard.MinLength = schema.DefaultPasswordPolicyConfiguration.Standard.MinLength
		} else if config.Standard.MinLength < 0 {
			validator.Push(fmt.Errorf(errFmtPasswordPolicyMinLengthNotGreaterThanZero, config.Standard.MinLength))
		}

		if config.Standard.MaxLength == 0 {
			config.Standard.MaxLength = schema.DefaultPasswordPolicyConfiguration.Standard.MaxLength
		}
	}
}
