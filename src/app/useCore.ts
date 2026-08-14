import { useQuery } from "@tanstack/react-query";
import { coreApi } from "../shared/api/client";
export function useCore() { return useQuery({ queryKey: ["core"], queryFn: () => coreApi.bootstrap(), refetchInterval: 15_000, retry: 1 }); }
