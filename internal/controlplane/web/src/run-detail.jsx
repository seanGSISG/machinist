import React from "react";
import { PageHeading } from "@/components/ui/page-heading";

export function RunDetail({ runID }) {
  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <PageHeading title="Run detail" description={`Run ${runID}`} />
  </div>;
}
