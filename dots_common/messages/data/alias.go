package data_messages

import (
  "fmt"
  log "github.com/sirupsen/logrus"
  types "github.com/nttdots/go-dots/dots_common/types/data"
  "github.com/nttdots/go-dots/dots_server/models"
  "github.com/nttdots/go-dots/dots_common/messages"
)

type AliasesRequest struct {
  Aliases types.Aliases `json:"ietf-dots-data-channel:aliases"`
}

type AliasesResponse struct {
  Aliases types.Aliases `json:"ietf-dots-data-channel:aliases"`
}

func (r *AliasesRequest) Validate(method string, customer *models.Customer) (bool, string) {
  errorMsg := ""

  if len(r.Aliases.Alias) <= 0 {
    log.WithField("len", len(r.Aliases.Alias)).Error("'alias' is not exist.")
    errorMsg = fmt.Sprintf("Body Data Error : 'alias' is not exist")
    return false, errorMsg
  }

  var aliasNameList []string
  for _,alias := range r.Aliases.Alias {
    if alias.Name == "" {
      log.Error("Missing required alias 'name' attribute.")
      errorMsg = fmt.Sprintf("Body Data Error : Missing alias 'name'")
      return false, errorMsg
    }

    if messages.Contains(aliasNameList, alias.Name) {
      log.Errorf("Duplicate alias 'name' = %+v", alias.Name)
      errorMsg = fmt.Sprintf("Body Data Error : Duplicate alias 'name'(%v)", alias.Name)
      return false, errorMsg
    }
    aliasNameList = append(aliasNameList, alias.Name)

    if alias.PendingLifetime != nil {
      log.WithField("pending-lifetime", *alias.PendingLifetime).Errorf("'pending-lifetime' found at alias 'name'=%+v.", alias.Name)
      errorMsg = fmt.Sprintf("Body Data Error : Found NoConfig Attribute 'pending-lifetime' (%v) at alias 'name'(%v)", *alias.PendingLifetime, alias.Name)
      return false, errorMsg
    }

    if len(alias.TargetPrefix) == 0 && len(alias.TargetFQDN) == 0 && len(alias.TargetURI) == 0 {
      log. Errorf("At least one of the 'target-prefix', 'target-fqdn', or 'target-uri' attributes MUST be present at alias 'name'=%+v.", alias.Name)
      errorMsg = fmt.Sprintf("Body Data Error : At least one of the 'target-prefix', 'target-fqdn', or 'target-uri' attributes MUST be present at alias 'name'=(%v)", alias.Name)
      return false, errorMsg
    }
    if method == "PUT" && len(alias.TargetPrefix) == 0 {
      log. Errorf("Missing required 'target-prefix' attribute at alias 'name'=%+v.", alias.Name)
      errorMsg = fmt.Sprintf("Body Data Error : Missing 'target-prefix' at alias 'name'(%v)", alias.Name)
      return false, errorMsg
    }

    for _,targetPrefix := range alias.TargetPrefix {
      prefix,_ := models.NewPrefix(targetPrefix.String())
      if prefix.IsMulticast() || prefix.IsLoopback() || prefix.IsBroadCast() {
        log.WithField("'target-prefix'", targetPrefix).Errorf("The prefix  MUST NOT include broadcast, loopback, or multicast addresses at alias 'name'=%+v", alias.Name)
        errorMsg = fmt.Sprintf("Body Data Error : The prefix MUST NOT include broadcast, loopback, or multicast addresses.'target-prefix'(%v) at alias 'name'(%v)", targetPrefix, alias.Name)
        return false, errorMsg
      }
      validAddress,addressRange := prefix.CheckValidRangeIpAddress(customer.CustomerNetworkInformation.AddressRange)
      if !validAddress {
        log. Errorf("'target-prefix'with value = %+v is not supported within Portal ex-portal1 %+v at alias 'name'=%+v", prefix, addressRange, alias.Name)
        errorMsg = fmt.Sprintf("Body Data Error : 'target-prefix' with value = %+v is not supported within Portal ex-portal1 (%v) at alias 'name'(%v)", prefix, addressRange, alias.Name)
        return false, errorMsg
      }
    }

    for _, pr := range alias.TargetPortRange {
      if pr.UpperPort != nil {
        if *pr.UpperPort < pr.LowerPort {
          log.WithField("lower-port", pr.LowerPort).WithField("upper-port", *pr.UpperPort).Errorf("'upper-port' must be greater than or equal to 'lower-port' at alias 'name'=%+v.", alias.Name)
          errorMsg = fmt.Sprintf("Body Data Error : 'upper-port' must be greater than or equal to 'lower-port' at alias 'name'(%v)", alias.Name)
          return false, errorMsg
        }
      }
    }
  }

  return true, ""
}

func (r *AliasesRequest) ValidateWithName(name string, method string, customer *models.Customer) (bool, string) {
  bValid, errorMsg := r.Validate(method, customer)
  if !bValid {
    return false, errorMsg
  }

  if len(r.Aliases.Alias) > 1 {
    log.WithField("len", len(r.Aliases.Alias)).Error("multiple 'alias'.")
    errorMsg = fmt.Sprintf("Body Data Error : Have multiple 'alias' (%d)", len(r.Aliases.Alias))
    return false, errorMsg
  }

  alias := r.Aliases.Alias[0]
  if alias.Name != name {
    log.WithField("name(req)", alias.Name).WithField("name(URI)", name).Error("request/URI name mismatch.")
    errorMsg = fmt.Sprintf("Request/URI name mismatch : (%v) / (%v)", alias.Name, name)
    return false, errorMsg
  }

  return true, ""
}