import { useEffect, useState } from "react";

export function useVisibleInterval(milliseconds: number): number | false {
  const [visible, setVisible] = useState(() => document.visibilityState === "visible");

  useEffect(() => {
    const update = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);

  return visible ? milliseconds : false;
}
