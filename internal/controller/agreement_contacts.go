package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

func AgreementPublicContacts(c *gin.Context) {
	const op = "fetch"
	s, ok := agreementStore(c, op)
	if !ok {
		return
	}
	contacts, err := s.PublicContacts()
	if err != nil {
		agreementFail(c, op, err)
		return
	}
	c.JSON(200, contacts)
}

var AgreementAdminContacts = agreementAdmin("public_contacts", logActionSearch, func(_ *gin.Context, s service.AgreementStore) (any, error) {
	return s.PublicContacts()
})
var UpdateAgreementAdminContacts = agreementAdmin("update_public_contacts", logActionUpdate, func(c *gin.Context, s service.AgreementStore) (any, error) {
	var input service.AgreementContacts
	if err := agreementJSON(c, &input); err != nil {
		return nil, err
	}
	return s.SetPublicContacts(input)
})
var AgreementDiagnosticTemplate = agreementAdmin("diagnostics_template", logActionSearch, func(_ *gin.Context, _ service.AgreementStore) (any, error) {
	return service.ApprovedDiagnosticAgreementTemplate(), nil
})
