"use strict";
collectFP("iframe").then((r) => parent.postMessage(r, location.origin));
