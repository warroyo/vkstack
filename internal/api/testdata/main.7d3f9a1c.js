/*
 * A trimmed stand-in for the SPA's main.<hash>.js. The live bundle is ~5.5 MB of minified
 * Angular; this keeps only the shapes the discovery regexes depend on.
 *
 * The key below is FAKE. The real one is public, but checking a copy into this repo is
 * exactly what discovery exists to avoid — do not paste the live value here.
 */
!function(e,t){"use strict";var n=function(e){this.http=e};
n.prototype.headers=function(){return{"Content-Type":"application/json","X-Auth-Key":"FAKEKEY-not-the-real-one-0000000000000000000000000000000000000"}};
/* Staging deliberately comes first: the regex must skip it and still find production. */
var i={production:!1,
  simServiceUrl:"https://interop.esp-stg.spespg1.vmw.saas.broadcom.com/external"};
var r={production:!0,
  simServiceUrl:"https://interop.esp.spespg1.vmw.saas.broadcom.com/external",
  compatServiceUrl:"https://compat.esp.spespg1.vmw.saas.broadcom.com/external"};
e.environment=r,e.stagingEnvironment=i,e.ApiService=n}(window,document);
