"use strict";
importScripts("/collect.js");
collectFP("Worker").then((r) => postMessage(r));
